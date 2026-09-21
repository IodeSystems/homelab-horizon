package apitypes

// Key custody: the recovery-recipient wire shapes.
//
// A recovery recipient is a recipient that is always approved: every
// environment key is wrapped to its PUBLIC key at the moment the key is
// created, so the key survives the loss of the one laptop whose ~/.hz tree
// holds it. See internal/config/recovery.go for the model, and for why both
// the recipients and the wraps live in the config rather than in the identity
// database.
//
// NOTHING in this file carries a private key, and nothing added to it may. hz
// never receives one, never writes one, and has no endpoint that would accept
// one — recovery is proved by `hz cm recovery verify` on the operator's own
// machine, which reads the private key from stdin and sends nothing back.

// CMRecoveryRecipient is one recipient as hz holds it.
type CMRecoveryRecipient struct {
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
	// Fingerprint is derived from PublicKey by hz, for display only. A caller
	// that is about to WRAP must derive it from the key bytes itself — a
	// fingerprint hz reports beside a key hz chose proves nothing, which is
	// the same reason cmPublicKey is a separate read from the queue listing.
	Fingerprint string `json:"fingerprint,omitempty"`
	AddedAt     string `json:"addedAt,omitempty"`
}

// CMRecoveryWrap is one environment key wrapped to one recovery recipient.
//
// Wrapped is served back to an admin on purpose: it is the blob `hz cm
// recovery verify` has to open to prove custody, and only the recovery private
// key opens it. hz already relays exactly this shape to a box at every boot.
type CMRecoveryWrap struct {
	Environment string `json:"environment"`
	App         string `json:"app"`
	Role        string `json:"role"`
	// KeyID is what the wrapper CLAIMED is inside. It is a claim, not a proof;
	// proving it is what verify does.
	KeyID       string `json:"keyId"`
	Recipient   string `json:"recipient"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Wrapped     string `json:"wrapped"`
	WrappedAt   string `json:"wrappedAt,omitempty"`
}

// CMRecoveryResp is the whole custody picture in one read: who the recipients
// are, and what is wrapped to each. One read rather than two, because the gap
// this exists to surface — "environment X has no wrap for recipient Y" — is a
// join of the two, and computing it from two reads taken at different moments
// is how a gap gets reported that had already closed.
type CMRecoveryResp struct {
	Recipients []CMRecoveryRecipient `json:"recipients"`
	Wraps      []CMRecoveryWrap      `json:"wraps"`
}

// CMRecoveryRecipientReq adds a recovery recipient, or rotates the public key
// of the one that already holds that name.
//
// PublicKey is public. It travels in a body rather than in argv for
// consistency with the rest of this surface, not because it is a secret.
type CMRecoveryRecipientReq struct {
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
}

// CMRecoveryWrapReq stores one wrap.
//
// hz verifies what it can before storing: that the blob decodes, that it is a
// KindWrappedEnvKey envelope, and that its recipient fingerprint is the
// fingerprint of the named recipient's public key. It cannot verify that the
// key INSIDE is the key KeyID names — nothing but the private half can — and
// that is exactly the hole `hz cm recovery verify` exists to close.
type CMRecoveryWrapReq struct {
	Environment string `json:"environment"`
	App         string `json:"app"`
	Role        string `json:"role"`
	KeyID       string `json:"keyId"`
	Recipient   string `json:"recipient"`
	Wrapped     string `json:"wrapped"`
	// Replace overwrites a wrap already held for this (address, key id,
	// recipient). Default false: a stored wrap is custody that has been, or
	// could be, verified, and a second wrap of the same key to the same
	// recipient opens to the same thing — so replacing by default would put
	// that at risk for no gain.
	Replace bool `json:"replace,omitempty"`
}

// CMRecoveryWrapResp reports what the store did. Stored is false when a wrap
// was already held and Replace was not set, which is the normal answer during
// a backfill and is not an error.
type CMRecoveryWrapResp struct {
	Stored bool           `json:"stored"`
	Wrap   CMRecoveryWrap `json:"wrap"`
}
