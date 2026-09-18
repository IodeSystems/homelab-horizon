package configmgr

// The machine protocol: the only messages that pass between hz and an agent on
// a box. These live outside internal/ because a client agent has to import
// them; the admin UI's DTOs stay in internal/apitypes, where hz can change them
// without breaking a fleet.
//
// NO value travels in the clear, secret or not. Since 2026-09-18 every config
// value is sealed client-side, so hz relays base64 envelopes it cannot open and
// holds no plaintext at all — see EncodeEnvelope. Key names and bindings are the
// only content hz reads, and they are what the promotion gate runs on.

// Registration states. A registration is pending the first time a
// (machine, app, environment, role) is seen and whenever its role changes,
// which are the two moments there is genuinely something new to bless. Every
// later boot finds itself already approved, so no human stands in a restart's
// path.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateDenied   = "denied"
)

// Bindings are promotion scope, and since 2026-09-18 that is all they are.
// Secrecy stopped being an axis when every value became sealed, so the four
// cells collapsed to two. These values must match internal/db's Binding
// constants exactly; they are the same vocabulary crossing a wire.
const (
	BindingInvariant = "invariant" // promotes, by client-side re-seal
	BindingEnv       = "env"       // environment-bound; must already be bound in the target
)

// RegisterRequest is what an agent posts at startup.
//
// PublicKey is generated on the box at first registration and its private half
// never leaves. Approval is the act of wrapping the environment key to it, so
// the identity being blessed and the identity that can decrypt are the same
// thing by construction rather than by a matching check somewhere.
type RegisterRequest struct {
	Machine     string `json:"machine"`
	Environment string `json:"environment"`
	App         string `json:"app"`
	Role        string `json:"role"`
	Version     string `json:"version"`
	PublicKey   string `json:"publicKey"` // MarshalMachinePublicKey form
}

// EnvKeyAddr is the address the wrapped environment key in the matching
// RegisterResponse must be authenticated against. It comes from the agent's own
// launch arguments, which is what makes authenticating it worth anything: hz
// files a relayed blob under whichever registration it likes, and a box may
// hold several.
func (r RegisterRequest) EnvKeyAddr() EnvKeyAddr {
	return EnvKeyAddr{Environment: r.Environment, App: r.App, Role: r.Role}
}

// RegisterResponse is hz's answer, polled until the state settles.
//
// WrappedEnvKey is present only once approved, and hz only ever relays it: it
// is minted in the approver's browser and stored as ciphertext. Fingerprint is
// echoed back so the agent can confirm hz recorded the public key it actually
// sent, and so an operator has the same string on both screens to compare.
type RegisterResponse struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Fingerprint string `json:"fingerprint,omitempty"`

	// MachineID is hz's identity for the box, and it is not decoration: it is
	// half of the authenticated address of every machine-scoped secret, so an
	// agent that does not know it cannot open one. Persist it beside the
	// private key at approval rather than re-reading it from each response —
	// authenticating an address a later answer could change would authenticate
	// nothing.
	MachineID string `json:"machineId,omitempty"`

	// WrappedEnvKey opens with UnwrapEnvKey at RegisterRequest.EnvKeyAddr() —
	// the address the agent asked for, never one read back out of this
	// response. A grant relayed into the wrong registration fails there.
	WrappedEnvKey string `json:"wrappedEnvKey,omitempty"` // base64 KindWrappedEnvKey envelope
}

// ConfigRequest asks for the config that resolves for one running process.
//
// Version is the clean semver the range test uses. Build carries the full
// `git describe` string for provenance only — it is not well ordered, so it
// must never take part in range containment.
type ConfigRequest struct {
	Machine     string `json:"machine"`
	Environment string `json:"environment"`
	App         string `json:"app"`
	Role        string `json:"role"`
	Version     string `json:"version"`
	Build       string `json:"build,omitempty"`
}

// Addr is the address the agent must pass to Open for one key of this response.
// It comes from the request the agent made, never from hz's answer: that is
// what makes it worth authenticating.
func (r ConfigRequest) Addr(key string) Addr {
	return Addr{Environment: r.Environment, App: r.App, Role: r.Role, Key: key}
}

// ConfigEntry is one resolved key. Sealed is never empty: there is no plaintext
// path, which is what stops a compromised hz choosing a value rather than
// merely relaying one.
//
// There is no field saying which key opens Sealed — the envelope's kind byte
// says so, it is covered by the AEAD, and a second copy on the wire could only
// ever disagree with it. Kind 0x01 opens with ConfigRequest.Addr(entry.Key);
// kind 0x03 opens with MachineAddr{Machine: the id learned at approval,
// Key: entry.Key}.
//
// An agent must still refuse an entry whose Key is absent from its own compiled
// schema, and refuse a schema key absent from Entries. Sealing every value
// stops hz inventing one; it does not stop hz omitting one, and an omitted key
// falls back to a compiled default — which is the founding bug this exists to
// prevent.
type ConfigEntry struct {
	Key     string `json:"key"`
	Binding string `json:"binding"`
	Sealed  string `json:"sealed"` // base64 envelope; never empty
}

// ConfigResponse is the winner of the resolution, and only the winner.
//
// Sequence is the blessing-time order that decided it. It is immutable, so an
// agent that reports the sequence it applied names exactly one config; that
// report is the only place the fact "v1.2.5 ran config 42" exists, because
// resolution is computed and never stored.
//
// Sequence is also what an agent enforces monotonicity against: cache the
// highest sequence ever applied AT THIS VERSION and refuse a lower one. hz
// serving an older blessed config authenticates perfectly — same address, same
// key name, same key id, so the AEAD cannot tell — and the agent's own cache is
// the only thing that knows better. Keyed by version so rolling a binary back
// still legitimately selects a lower sequence.
type ConfigResponse struct {
	ConfigID string        `json:"configId"`
	Sequence int64         `json:"sequence"`
	MinVer   string        `json:"minVer"`
	MaxVer   string        `json:"maxVer,omitempty"` // empty means open-ended
	Entries  []ConfigEntry `json:"entries"`
}
