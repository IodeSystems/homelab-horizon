package offsite

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// DefaultProfile is the AWS shared-config profile push uses when neither
// --aws-profile nor AWS_PROFILE names one.
const DefaultProfile = "hz-backup"

// ObjectSuffix and SidecarSuffix name the two objects of one backup.
const (
	ObjectSuffix  = ".hzbak"
	SidecarSuffix = ".json"
)

// Sidecar is the cleartext header uploaded beside each .hzbak object. Every
// field is public: it says which object it describes, what is in it, its
// sha256 and who can open it. It carries no secret and no wrapped key —
// those are inside the object.
type Sidecar struct {
	Format           int            `json:"format"`
	Object           string         `json:"object"`
	BackupID         string         `json:"backup_id"`
	HZVersion        string         `json:"hz_version"`
	Host             string         `json:"host"`
	SchemaVersion    uint           `json:"schema_version"`
	SnapshotAt       time.Time      `json:"snapshot_at"`
	CreatedAt        time.Time      `json:"created_at"`
	CiphertextSHA256 string         `json:"ciphertext_sha256"`
	CiphertextBytes  int64          `json:"ciphertext_bytes"`
	Recipients       []RecipientRef `json:"recipients"`
}

// RecipientRef names a recipient by name and public-key fingerprint only.
type RecipientRef struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
}

// Dest is a parsed s3://bucket/prefix.
type Dest struct {
	Bucket string
	Prefix string
}

// ParseDest reads s3://<bucket>[/<prefix>].
func ParseDest(s string) (Dest, error) {
	rest, ok := strings.CutPrefix(s, "s3://")
	if !ok {
		return Dest{}, fmt.Errorf("--dest %q: want s3://<bucket>/<prefix>", s)
	}
	bucket, prefix, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return Dest{}, fmt.Errorf("--dest %q: no bucket", s)
	}
	return Dest{Bucket: bucket, Prefix: strings.Trim(prefix, "/")}, nil
}

func (d Dest) String() string {
	if d.Prefix == "" {
		return "s3://" + d.Bucket
	}
	return "s3://" + d.Bucket + "/" + d.Prefix
}

// HostLabel reduces a hostname to characters safe in an object key and a
// unit name.
func HostLabel(h string) string {
	h = regexp.MustCompile(`[^A-Za-z0-9.-]+`).ReplaceAllString(h, "_")
	if h == "" {
		return "hz"
	}
	return h
}

// ObjectKey is <prefix>/<host>/<UTC time> without a suffix.
func (d Dest) ObjectKey(host string, at time.Time) string {
	k := HostLabel(host) + "/" + at.UTC().Format("20060102T150405Z")
	if d.Prefix != "" {
		k = d.Prefix + "/" + k
	}
	return k
}

// LoadAWSConfig resolves credentials and region through the AWS SDK's DEFAULT
// chain, with profile selecting the shared-config profile. The same code path
// serves a static-key user profile and an assumed-role profile
// (role_arn + credential_source = Ec2InstanceMetadata, or source_profile).
// Credentials never come from hz's own config.
//
// opts exist for tests (shared-config file locations); production passes none.
func LoadAWSConfig(ctx context.Context, profile, region string, opts ...func(*awsconfig.LoadOptions) error) (aws.Config, error) {
	if profile == "" {
		profile = DefaultProfile
	}
	all := []func(*awsconfig.LoadOptions) error{awsconfig.WithSharedConfigProfile(profile)}
	if region != "" {
		all = append(all, awsconfig.WithRegion(region))
	}
	all = append(all, opts...)
	cfg, err := awsconfig.LoadDefaultConfig(ctx, all...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("AWS profile %q: %w", profile, err)
	}
	if cfg.Region == "" {
		return aws.Config{}, fmt.Errorf("AWS profile %q names no region: set `region = <bucket's region>` in it, or pass --region", profile)
	}
	return cfg, nil
}

// NewS3Client is the production client: the SDK's own endpoint resolution,
// which is HTTPS. There is deliberately no endpoint override here and no
// flag that reaches one — the bucket policy denies non-TLS requests, and an
// override is the only way the SDK would send plain HTTP.
func NewS3Client(cfg aws.Config) *s3.Client {
	return s3.NewFromConfig(cfg)
}

// PutObjectAPI is the one S3 call push makes. The principal it runs as is
// put-only by design, so this interface has nothing else in it.
type PutObjectAPI interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// PushDeps lets a test hand push a client and a temp directory. Production
// leaves both zero: the client is NewS3Client's.
type PushDeps struct {
	API    PutObjectAPI
	TmpDir string
}

// PushResult is what a push uploaded.
type PushResult struct {
	Object  string
	Sidecar string
	Header  Sidecar
}

// Push encrypts zip to every recipient and uploads it to dest, then the
// cleartext sidecar. The ciphertext goes first: a sidecar without its object
// is detectable, an object without a sidecar is still a backup, and neither
// is a partial object — S3 PutObject is all-or-nothing.
//
// tmpDir is where the ciphertext is staged (0600) before upload; "" is the
// system temp directory.
func Push(ctx context.Context, api PutObjectAPI, dest Dest, zip []byte, meta Meta, recipients []Recipient, now time.Time, tmpDir string) (*PushResult, error) {
	if len(recipients) == 0 {
		return nil, ErrNoRecipients
	}
	f, err := os.CreateTemp(tmpDir, "hz-offsite-*.hzbak")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err := f.Chmod(0o600); err != nil {
		return nil, err
	}
	sh, md := sha256.New(), md5.New()
	h, err := Encrypt(io.MultiWriter(f, sh, md), bytes.NewReader(zip), meta, recipients)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	base := dest.ObjectKey(meta.Host, now)
	objKey, sideKey := base+ObjectSuffix, base+SidecarSuffix
	if err := put(ctx, api, dest.Bucket, objKey, f, size, "application/octet-stream", sh, md); err != nil {
		return nil, fmt.Errorf("upload s3://%s/%s: %w", dest.Bucket, objKey, err)
	}

	side := Sidecar{
		Format:           FormatVersion,
		Object:           objKey,
		BackupID:         h.BackupID,
		HZVersion:        meta.HZVersion,
		Host:             meta.Host,
		SchemaVersion:    meta.SchemaVersion,
		SnapshotAt:       meta.SnapshotAt,
		CreatedAt:        now.UTC(),
		CiphertextSHA256: hex.EncodeToString(sh.Sum(nil)),
		CiphertextBytes:  size,
	}
	for _, r := range h.Recipients {
		side.Recipients = append(side.Recipients, RecipientRef{Name: r.Name, Fingerprint: r.Fingerprint})
	}
	sideJSON, err := json.MarshalIndent(side, "", "  ")
	if err != nil {
		return nil, err
	}
	ssh, smd := sha256.New(), md5.New()
	ssh.Write(sideJSON)
	smd.Write(sideJSON)
	if err := put(ctx, api, dest.Bucket, sideKey, bytes.NewReader(sideJSON), int64(len(sideJSON)), "application/json", ssh, smd); err != nil {
		return nil, fmt.Errorf("the backup object s3://%s/%s IS uploaded and verified, but its header was not: %w", dest.Bucket, objKey, err)
	}
	return &PushResult{Object: objKey, Sidecar: sideKey, Header: side}, nil
}

// ErrChecksumMismatch: S3 answered a PutObject with a checksum that is not
// the one of the bytes hz sent.
var ErrChecksumMismatch = errors.New("S3's checksum of the stored object is not the sha256 of what was sent")

// put uploads one object and verifies it landed intact WITHOUT reading it
// back.
//
// This is the instrument (CLAUDE.md #15). The principal is put-only, so a
// GetObject read-back is impossible by design; what S3 can tell a writer is
// the checksum of what it stored. The request carries x-amz-checksum-sha256,
// which S3 verifies on receipt (a mismatch is refused there), and the
// response's ChecksumSHA256 must then EQUAL the local ciphertext's sha256.
// A response with no ChecksumSHA256 falls back to the ETag, which for a
// single-part upload without SSE-KMS is the MD5 of the body; when neither
// matches, the upload is a failure even though S3 said 200.
func put(ctx context.Context, api PutObjectAPI, bucket, key string, body io.ReadSeeker, size int64, contentType string, sh, md hash.Hash) error {
	want := base64.StdEncoding.EncodeToString(sh.Sum(nil))
	out, err := api.PutObject(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(bucket),
		Key:               aws.String(key),
		Body:              body,
		ContentLength:     aws.Int64(size),
		ContentType:       aws.String(contentType),
		ChecksumAlgorithm: s3types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(want),
	})
	if err != nil {
		return err
	}
	return checkPut(out, want, hex.EncodeToString(md.Sum(nil)))
}

func checkPut(out *s3.PutObjectOutput, wantSHA256, wantMD5 string) error {
	if got := aws.ToString(out.ChecksumSHA256); got != "" {
		if got != wantSHA256 {
			return fmt.Errorf("%w: S3 says sha256 %s, sent %s", ErrChecksumMismatch, got, wantSHA256)
		}
		return nil
	}
	etag := strings.Trim(aws.ToString(out.ETag), `"`)
	if etag != "" && etag == wantMD5 {
		return nil
	}
	return fmt.Errorf("%w: S3 returned no ChecksumSHA256 and its ETag %q is not the MD5 %s of what was sent", ErrChecksumMismatch, etag, wantMD5)
}
