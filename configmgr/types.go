package configmgr

// The machine protocol: the only messages that pass between hz and an agent on
// a box. These live outside internal/ because a client agent has to import
// them; the admin UI's DTOs stay in internal/apitypes, where hz can change them
// without breaking a fleet.
//
// Nothing here carries a secret value in the clear. Secrets travel as the
// base64 of an envelope hz cannot open — see EncodeEnvelope.

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

// Bindings. See the per-key metadata table in plan/config-manager.md: a secret
// is not a separate system, it is an environment-bound key that hz may not
// read.
const (
	BindingInvariant   = "invariant"
	BindingEnvironment = "environment"
	BindingSecret      = "secret"
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

// ConfigEntry is one resolved key.
//
// Value and Sealed are mutually exclusive: a secret arrives sealed, everything
// else arrives in the clear because promotion has to diff it. There is no field
// saying which key opens Sealed — the envelope's kind byte says so, it is
// covered by the AEAD, and a second copy on the wire could only ever disagree
// with it.
type ConfigEntry struct {
	Key     string `json:"key"`
	Binding string `json:"binding"`
	Value   string `json:"value,omitempty"`
	Sealed  string `json:"sealed,omitempty"` // base64 envelope
}

// ConfigResponse is the winner of the resolution, and only the winner.
//
// Sequence is the blessing-time order that decided it. It is immutable, so an
// agent that reports the sequence it applied names exactly one config; that
// report is the only place the fact "v1.2.5 ran config 42" exists, because
// resolution is computed and never stored.
type ConfigResponse struct {
	ConfigID string        `json:"configId"`
	Sequence int64         `json:"sequence"`
	MinVer   string        `json:"minVer"`
	MaxVer   string        `json:"maxVer,omitempty"` // empty means open-ended
	Entries  []ConfigEntry `json:"entries"`
}
