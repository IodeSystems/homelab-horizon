// Package configmgr holds the cryptographic core and the machine protocol of
// hz's config manager. It sits outside internal/ on purpose: a client agent on
// every box has to import it, and a security primitive that each client
// reimplements is a security primitive with several subtly different versions.
//
// # What this package is defending
//
// hz is a network appliance. Making it the config store means a compromise of
// hz would otherwise yield every credential on every box, which is a material
// escalation over yielding DNS, VPN and proxy control. So hz stores secret
// values as ciphertext it cannot read: it holds the blob, the metadata, the
// version range and the approval state, and only a holder of the environment
// key can decrypt. A compromise leaks ciphertext and a fleet map.
//
// Keys are symmetric and per environment, because an operator must be able to
// paste a key into the UI and SEE existing values to debug a delta between
// environments. Encrypt-to-public-key would let an admin write new values and
// never read old ones, which removes the feature that motivated the work.
//
// hz never holds an environment key. The approver does, and distributes it
// through the act of approving: the agent presents a public key at
// registration, the approver's browser wraps the environment key to it, and hz
// relays a blob it cannot open. Approval is therefore a cryptographic
// capability grant, not an authorization flag — an unapproved box holding every
// blob in the database can decrypt none of them, and no bug in an authorization
// check can change that.
//
// A secret may instead be bound to a single machine, sealed directly to that
// machine's public key with no environment key in the path. That is what makes
// per-device revocation expressible: deleting the blob revokes it on one box
// and nowhere else.
//
// The full model is in plan/config-manager.md.
//
// # Primitive choices
//
// The browser is a mandatory participant — it is where the environment key is
// pasted, where wrapping happens, and where an operator decrypts to inspect a
// delta. So every primitive here must exist natively in BOTH WebCrypto and the
// Go standard library, with no library to audit on either side:
//
//   - ECDH on NIST P-256 (crypto/ecdh)
//   - HKDF-SHA256 (crypto/hkdf)
//   - AES-256-GCM (crypto/aes, crypto/cipher)
//
// # Deviation from the plan: P-256 rather than X25519
//
// plan/config-manager.md specifies X25519. This package uses ECDH on P-256
// instead. X25519 is the better curve on the merits, but WebCrypto's support
// for it is recent and uneven across browsers and versions, while ECDH P-256 is
// universally available. The plan's load-bearing claim is that decryption
// happens in the browser; a curve that some of the fleet's browsers cannot do
// natively would be paid for either in a JavaScript curve implementation nobody
// here can audit, or in the key reaching hz. P-256 is the choice that keeps the
// security property. Both sides are stdlib, both are constant time, and the
// construction below does not depend on which curve it runs over.
//
// # Envelope formats
//
// Every ciphertext is a self-describing byte string: version, kind, routing,
// nonce, ciphertext with its tag appended. It is a BLOB in sqlite and base64 on
// the wire (see EncodeEnvelope). One rule covers both layouts: the AEAD's
// additional data is every byte of the envelope before the nonce.
//
// Kind 0x01, a value sealed under an environment key. 38 bytes plus plaintext:
//
//	offset  size  field
//	0       1     version, 0x01
//	1       1     kind, 0x01
//	2       8     key id
//	10      12    nonce
//	22      ..    AES-256-GCM ciphertext || 16-byte tag
//	              AAD = bytes 0..9
//
// Kinds 0x02 and 0x03, ephemeral-ECDH to a machine's public key — 0x02 carries
// a wrapped environment key, 0x03 a machine-scoped secret. 107 bytes plus
// plaintext, so a wrapped environment key is exactly 139 bytes:
//
//	offset  size  field
//	0       1     version, 0x01
//	1       1     kind, 0x02 or 0x03
//	2       12    recipient fingerprint
//	14      65    ephemeral public key, SEC1 uncompressed
//	79      12    nonce
//	91      ..    AES-256-GCM ciphertext || 16-byte tag
//	              AAD = bytes 0..78
//
// The environment key is used as the AES key directly. It is already 32
// uniformly random bytes, so a KDF on that path would only add a step the
// browser must replicate exactly.
//
// For kinds 0x02 and 0x03 the AES key is derived as:
//
//	shared = ECDH(ephemeral private, recipient public)      // 32-byte X coordinate
//	salt   = "hz-config/v1"
//	info   = purpose || 0x00 || ephemeral public || recipient public
//	key    = HKDF-SHA256(shared, salt, info, 32)
//
// where purpose is "hz-config/v1 wrap-env-key" for kind 0x02 and
// "hz-config/v1 machine-secret" for kind 0x03.
//
// # Deriving the identifiers
//
// A browser that mints envelopes has to compute these itself. All three are a
// SHA-256 over an ASCII label concatenated with the input, truncated. No label
// is a prefix of another, which is what makes label||input unambiguous without
// a length field:
//
//	key id      = SHA-256("hz-config/v1 env-key-id" || envKey)[:8]
//	fingerprint = SHA-256("hz-config/v1 machine-fingerprint" || recipientSEC1)[:12]
//	checksum    = SHA-256("hz-config/v1 env-key-checksum" || envKey)[:4]
//
// The text an operator pastes is EnvKeyPrefix followed by the Crockford base32
// (alphabet "0123456789ABCDEFGHJKMNPQRSTVWXYZ", no padding) of
// envKey || checksum — 32 + 4 bytes, so 58 characters. A page that parses one
// must fold case, fold O to 0 and I or L to 1, strip dashes and spaces, reject
// any length but 58, verify the checksum, and reject a non-canonical encoding
// by re-encoding the decoded bytes and comparing. Without that last check a
// corruption in the final character decodes to the same key and goes unnoticed.
//
// # Interoperating from the browser
//
// The exact WebCrypto parameters, which must match byte for byte:
//
//   - Import a machine public key: crypto.subtle.importKey("raw", sec1Bytes,
//     {name: "ECDH", namedCurve: "P-256"}, false, []) — sec1Bytes is the
//     65-byte uncompressed point, 0x04 || X || Y.
//   - Ephemeral pair: crypto.subtle.generateKey({name: "ECDH", namedCurve:
//     "P-256"}, false, ["deriveBits"]); export its public half with
//     exportKey("raw") to get the 65 bytes that ride in the envelope.
//   - Shared secret: crypto.subtle.deriveBits({name: "ECDH", public:
//     recipientKey}, ephemeralPrivate, 256) — 32 bytes, the X coordinate only,
//     which is what Go's ecdh.PrivateKey.ECDH returns for NIST curves.
//   - HKDF: importKey("raw", shared, "HKDF", false, ["deriveBits"]), then
//     deriveBits({name: "HKDF", hash: "SHA-256", salt: utf8("hz-config/v1"),
//     info: infoBytes}, hkdfKey, 256). salt and info are byte arrays, info is
//     the concatenation above, and the length is in BITS.
//   - AES: importKey("raw", key32, {name: "AES-GCM"}, false, ["encrypt",
//     "decrypt"]), then encrypt({name: "AES-GCM", iv: nonce12,
//     additionalData: headerBytes, tagLength: 128}, aesKey, plaintext).
//     WebCrypto appends the 16-byte tag to the ciphertext, which is the same
//     layout Go's cipher.AEAD produces, so no splicing is needed on either
//     side.
//
// The nonce is 12 fresh random bytes for every seal — crypto.getRandomValues —
// and never a counter. A repeated nonce under one AES-GCM key leaks the XOR of
// the two plaintexts and the GHASH subkey, after which forged ciphertext
// verifies.
//
// The environment key must live in page memory only: never localStorage, never
// sessionStorage, cleared on navigate, with an explicit lock. hz may log that a
// decrypt session was opened and by whom. It must never see the key.
package configmgr
