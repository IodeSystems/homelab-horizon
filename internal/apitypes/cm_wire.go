package apitypes

import "github.com/iodesystems/homelab-horizon/configmgr"

// Wire-name constants for the config manager's admin endpoints.
//
// ⚠ THIS FILE IS EXCLUDED FROM tygo (tygo.yaml exclude_files).
//
// These are ALIASES of constants in configmgr, and stock tygo cannot follow an alias into
// another package: it parses one package and emits its fallback TYPE where the value goes,
//
//	export const CMQueryEnv = any /* configmgr.QueryEnv */;
//
// which is not valid TypeScript. hz now pins a fork that resolves constants through
// go/types (gzuidhof/tygo#100), so these WOULD generate correctly today. The exclusion
// stays for a different reason: the UI references none of these eleven names — they are
// Go-side wire constants shared by the server and the CLI, and the browser is not a party
// to them. Generating them would ship dead weight and invite someone to use them.
//
// Duplicating the literals here instead was rejected: that reintroduces the second
// definition whose drift broke five endpoints.
const (
	CMQueryEnv      = configmgr.QueryEnv
	CMQueryApp      = configmgr.QueryApp
	CMQueryRole     = configmgr.QueryRole
	CMQueryVersion  = configmgr.QueryVersion
	CMQueryConfigID = configmgr.QueryConfigID
	CMQueryTarget   = configmgr.QueryTarget
	CMQueryState    = configmgr.QueryState

	CMPathResolve     = configmgr.PathResolve
	CMPathPromoteGate = configmgr.PathPromoteGate
	CMPathCurrentKey  = configmgr.PathCurrentKey
	CMPathConfigs     = configmgr.PathConfigs
)
