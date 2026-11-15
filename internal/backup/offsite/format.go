// Package offsite is hz's encrypted, off-site backup: the verified backup zip
// (internal/backup), encrypted on the box to every recovery recipient and
// uploaded with s3:PutObject only.
//
// It runs ON the box whose hz.db it is (`homelab-horizon backup push`, root,
// console). hz stays outbound-only (CLAUDE.md #1): it writes to a bucket with
// a put-only principal and never holds a credential that reads one. Restores
// read through the operator's own session, off the box.
//
// What crosses to the bucket is CIPHERTEXT ONLY, plus a cleartext sidecar
// header that names the object, its sha256 and its recipients' fingerprints
// and carries no secret. The data key is wrapped to each recovery recipient
// with configmgr.WrapBackupKey — the recovery-wrap construction, kind 0x04 —
// so whoever holds a recovery private key opens it, and hz, which holds no
// recovery private key (CLAUDE.md #4), cannot.
//
// # File format (.hzbak)
//
//	offset  size  field
//	0       8     magic "hzbak/1\n"
//	8       4     header length N, uint32 big-endian (at most 1 MiB)
//	12      N     header, JSON (FileHeader): no secret; the data key appears
//	              only wrapped, once per recipient
//	12+N    ..    chunks: AES-256-GCM under the data key, each ChunkSize
//	              plaintext bytes (the last 0..ChunkSize) plus a 16-byte tag
//
// Chunk i's nonce is 3 zero bytes || uint64be(i) || final, where final is 1
// on the last chunk and 0 on every other. Every chunk's additional data is
// sha256(magic || length || header), so the header — recipients, backup id,
// metadata — is authenticated by every chunk. A reordered, dropped or
// appended chunk, a file cut at a chunk boundary, and a changed header byte
// all fail authentication. The data key is fresh for each file, so a counter
// nonce never repeats under one key.
package offsite

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Magic opens every .hzbak file.
const Magic = "hzbak/1\n"

// FormatVersion is FileHeader.Format and Sidecar.Format.
const FormatVersion = 1

// ChunkSize is the plaintext bytes per chunk.
const ChunkSize = 1 << 20

const (
	tagSize        = 16
	maxHeaderBytes = 1 << 20
	cipherName     = "AES-256-GCM, chunked"
)

// Errors a caller branches on.
var (
	// ErrNoRecipients: there is nobody to encrypt to, so nothing is written.
	ErrNoRecipients = errors.New("this hz has no recovery recipient, so a backup would be either plaintext or something nobody can decrypt; hz uploads neither. " +
		"Mint one with `hz config recovery keygen --name <name>` and register it with `hz config recovery add <name> --public-key <hzmpub_...>`")
	// ErrNotForThisKey: no recipient in the file matches the key given.
	ErrNotForThisKey = errors.New("this backup is not encrypted to that recovery key")
	// ErrAuthentication: the file was changed, cut or reordered, or the
	// header was edited. No plaintext from it is trustworthy.
	ErrAuthentication = errors.New("backup failed authentication: it was altered or truncated")
)

// Recipient is one public key the data key is wrapped to.
type Recipient struct {
	Name      string
	PublicKey *ecdh.PublicKey
}

// RecipientsFromConfig parses every recovery recipient in cfg. An empty list
// is ErrNoRecipients; a key that does not parse is an error, never skipped.
func RecipientsFromConfig(cfg *config.Config) ([]Recipient, error) {
	if len(cfg.RecoveryRecipients) == 0 {
		return nil, ErrNoRecipients
	}
	out := make([]Recipient, 0, len(cfg.RecoveryRecipients))
	for _, r := range cfg.RecoveryRecipients {
		pub, err := configmgr.ParseMachinePublicKey(r.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("recovery recipient %q: %w", r.Name, err)
		}
		out = append(out, Recipient{Name: r.Name, PublicKey: pub})
	}
	return out, nil
}

// Meta is what a backup says about itself, from the zip's manifest.
type Meta struct {
	HZVersion     string    `json:"hz_version"`
	Host          string    `json:"host"`
	SchemaVersion uint      `json:"schema_version"`
	SnapshotAt    time.Time `json:"snapshot_at"`
}

// WrappedKey is the data key wrapped to one recipient.
type WrappedKey struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	// Wrapped is a kind 0x04 envelope (configmgr.EncodeEnvelope form). It
	// opens only with that recipient's private key.
	Wrapped string `json:"wrapped"`
}

// FileHeader is the JSON header inside a .hzbak file.
type FileHeader struct {
	Format int `json:"format"`
	// BackupID is random, and is the address the data key is wrapped at.
	BackupID   string       `json:"backup_id"`
	Cipher     string       `json:"cipher"`
	ChunkSize  int          `json:"chunk_size"`
	Meta       Meta         `json:"meta"`
	Recipients []WrappedKey `json:"recipients"`
}

// Encrypt writes plaintext to w as a .hzbak file encrypted to every
// recipient, and returns the header it wrote.
func Encrypt(w io.Writer, plaintext io.Reader, meta Meta, recipients []Recipient) (*FileHeader, error) {
	if len(recipients) == 0 {
		return nil, ErrNoRecipients
	}
	dataKey := make([]byte, configmgr.BackupKeySize)
	if _, err := rand.Read(dataKey); err != nil {
		return nil, err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	h := &FileHeader{
		Format:    FormatVersion,
		BackupID:  hex.EncodeToString(id),
		Cipher:    cipherName,
		ChunkSize: ChunkSize,
		Meta:      meta,
	}
	for _, r := range recipients {
		blob, err := configmgr.WrapBackupKey(r.PublicKey, configmgr.BackupAddr{BackupID: h.BackupID}, dataKey)
		if err != nil {
			return nil, fmt.Errorf("wrap to %q: %w", r.Name, err)
		}
		h.Recipients = append(h.Recipients, WrappedKey{
			Name:        r.Name,
			Fingerprint: configmgr.FingerprintOf(r.PublicKey).String(),
			Wrapped:     configmgr.EncodeEnvelope(blob),
		})
	}
	hdr, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	preamble := make([]byte, 0, len(Magic)+4+len(hdr))
	preamble = append(preamble, Magic...)
	preamble = binary.BigEndian.AppendUint32(preamble, uint32(len(hdr)))
	preamble = append(preamble, hdr...)
	if _, err := w.Write(preamble); err != nil {
		return nil, err
	}
	ad := sha256.Sum256(preamble)
	aead := gcm(dataKey)

	br := bufio.NewReaderSize(plaintext, ChunkSize)
	buf := make([]byte, ChunkSize)
	out := make([]byte, 0, ChunkSize+tagSize)
	for i := uint64(0); ; i++ {
		n, err := io.ReadFull(br, buf)
		final := false
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			final = true
		case err != nil:
			return nil, err
		default:
			// A full chunk is the last one only when nothing follows it.
			if _, perr := br.Peek(1); errors.Is(perr, io.EOF) {
				final = true
			} else if perr != nil {
				return nil, perr
			}
		}
		out = aead.Seal(out[:0], nonce(i, final), buf[:n], ad[:])
		if _, err := w.Write(out); err != nil {
			return nil, err
		}
		if final {
			return h, nil
		}
	}
}

// ReadHeader reads a .hzbak file's magic and header and returns the header
// with the preamble's bytes (the chunks' additional data is their sha256).
func ReadHeader(r io.Reader) (*FileHeader, []byte, error) {
	pre := make([]byte, len(Magic)+4)
	if _, err := io.ReadFull(r, pre); err != nil {
		return nil, nil, fmt.Errorf("not an hz backup: %w", err)
	}
	if string(pre[:len(Magic)]) != Magic {
		return nil, nil, errors.New("not an hz backup: wrong magic")
	}
	n := binary.BigEndian.Uint32(pre[len(Magic):])
	if n == 0 || n > maxHeaderBytes {
		return nil, nil, fmt.Errorf("not an hz backup: header length %d", n)
	}
	hdr := make([]byte, n)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, nil, fmt.Errorf("header: %w", err)
	}
	var h FileHeader
	dec := json.NewDecoder(bytes.NewReader(hdr))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return nil, nil, fmt.Errorf("header: %w", err)
	}
	if h.Format != FormatVersion {
		return nil, nil, fmt.Errorf("backup format %d; this hz reads format %d", h.Format, FormatVersion)
	}
	if h.ChunkSize <= 0 || h.ChunkSize > 64<<20 {
		return nil, nil, fmt.Errorf("header: chunk size %d", h.ChunkSize)
	}
	return &h, append(pre, hdr...), nil
}

// Decrypt reads a .hzbak file and writes its plaintext to w. Plaintext is
// written chunk by chunk as each authenticates, so on an error w holds a
// prefix that must be discarded: write to a temporary file and keep it only
// when Decrypt returns nil.
func Decrypt(r io.Reader, priv *ecdh.PrivateKey, w io.Writer) (*FileHeader, error) {
	h, preamble, err := ReadHeader(r)
	if err != nil {
		return nil, err
	}
	mine := configmgr.FingerprintOf(priv.PublicKey()).String()
	var dataKey []byte
	for _, rk := range h.Recipients {
		if rk.Fingerprint != mine {
			continue
		}
		blob, err := configmgr.DecodeEnvelope(rk.Wrapped)
		if err != nil {
			return nil, fmt.Errorf("recipient %q: %w", rk.Name, err)
		}
		if dataKey, err = configmgr.UnwrapBackupKey(priv, configmgr.BackupAddr{BackupID: h.BackupID}, blob); err != nil {
			return nil, fmt.Errorf("recipient %q: %w: %v", rk.Name, ErrAuthentication, err)
		}
		break
	}
	if dataKey == nil {
		return nil, fmt.Errorf("%w (key fingerprint %s; the file is for %s)", ErrNotForThisKey, mine, fingerprints(h))
	}
	ad := sha256.Sum256(preamble)
	aead := gcm(dataKey)

	br := bufio.NewReaderSize(r, h.ChunkSize+tagSize)
	buf := make([]byte, h.ChunkSize+tagSize)
	var pt []byte
	for i := uint64(0); ; i++ {
		n, err := io.ReadFull(br, buf)
		final := false
		switch {
		case errors.Is(err, io.EOF):
			return nil, fmt.Errorf("%w: the file ends before its final chunk", ErrAuthentication)
		case errors.Is(err, io.ErrUnexpectedEOF):
			final = true
		case err != nil:
			return nil, err
		default:
			if _, perr := br.Peek(1); errors.Is(perr, io.EOF) {
				final = true
			} else if perr != nil {
				return nil, perr
			}
		}
		if n < tagSize {
			return nil, fmt.Errorf("%w: chunk %d is %d bytes", ErrAuthentication, i, n)
		}
		pt, err = aead.Open(pt[:0], nonce(i, final), buf[:n], ad[:])
		if err != nil {
			return nil, fmt.Errorf("%w: chunk %d", ErrAuthentication, i)
		}
		if _, err := w.Write(pt); err != nil {
			return nil, err
		}
		if final {
			return h, nil
		}
	}
}

func fingerprints(h *FileHeader) string {
	var b bytes.Buffer
	for i, r := range h.Recipients {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s %s", r.Name, r.Fingerprint)
	}
	return b.String()
}

func nonce(i uint64, final bool) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[3:11], i)
	if final {
		n[11] = 1
	}
	return n
}

func gcm(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic("offsite: aes: " + err.Error()) // the key is always 32 bytes
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("offsite: gcm: " + err.Error())
	}
	return aead
}
