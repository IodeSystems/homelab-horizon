package apitypes

// InstancesPath is the list of hz instances: this gateway, every HA peer it
// knows about (config.Peers), and every declared Machine that runs its own hz
// (config.Machine.HZ, role "nested"). One row per hz, not per machine and not per
// address.
const InstancesPath = "/api/v1/instances"

// Instance roles. Standalone is "no peers declared" — the fleet does not
// exist, so there is no primary to be or to follow.
const (
	InstanceRolePrimary    = "primary"
	InstanceRoleReplica    = "replica"
	InstanceRoleStandalone = "standalone"
	// InstanceRoleNested is a SEPARATE hz — its own records and keys, a
	// config layer of its own — declared here as a Machine with an hz marker
	// (config.Machine.HZ). Not a cluster member: it shares nothing with this
	// instance's records. Address is its HZ.URL; nothing here contacts it.
	InstanceRoleNested = "nested"
)

// InstanceResp is one hz instance.
//
// Name is the SELF row's machine name as `machines/add` with self=true
// resolves it (server.LocalMachineName) — never a name the browser supplied —
// and a peer row's peer ID. Project and Declared are the join against the
// Machine record of that exact name: Declared=false means no record exists,
// so Project is "" because there is nothing to read it from, not because the
// owner is global. The UI must say which.
type InstanceResp struct {
	Name string `json:"name"`
	Self bool   `json:"self"`
	// Address is the self row's local_interface and a peer's wg_addr. Empty
	// on self means hz has not detected its own LAN address.
	Address string `json:"address"`
	// Role is InstanceRolePrimary, InstanceRoleReplica or InstanceRoleStandalone.
	Role string `json:"role"`
	// PeerID is the fleet identity: config.PeerID on self, Peer.ID on a peer.
	// Empty on a standalone self with no peer_id set.
	PeerID string `json:"peerId"`
	// PrimaryID is the peer ID of the fleet's config primary, the same on
	// every row. Empty when standalone, or when no instance is marked primary.
	PrimaryID string `json:"primaryId"`
	// Project is the owning project of the Machine record named Name. Always
	// sent; "" is global when Declared, and "no record" when not.
	Project  string `json:"project"`
	Declared bool   `json:"declared"`
	// Version is the running hz version. Self only — hz does not ask a peer.
	Version string `json:"version,omitempty"`
	// Sync is the replica's pull-loop state. Present only on the self row of a
	// replica; absent everywhere else, which is "not a replica here", not
	// "never synced" (that is Sync present with LastSuccessAt 0).
	Sync *InstanceSyncResp `json:"sync,omitempty"`
}

// InstanceSyncResp is a replica's last pull from the config primary.
type InstanceSyncResp struct {
	PullCount     int    `json:"pullCount"`
	LastSuccessAt int64  `json:"lastSuccessAt"` // unix seconds, 0 if never
	LastError     string `json:"lastError"`     // "" when the last attempt succeeded
}
