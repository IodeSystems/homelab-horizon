package apitypes

import "github.com/iodesystems/homelab-horizon/configmgr"

// Wire-name constants for the config manager's admin endpoints.
//
// ⚠ THIS FILE IS EXCLUDED FROM tygo (tygo.yaml exclude_files), and the exclusion is
// load-bearing rather than tidiness. These are ALIASES of constants in configmgr, and tygo
// parses only this package — it cannot resolve an identifier from another one, so it emits
//
//     export const CMQueryEnv = any /* configmgr.QueryEnv */;
//
// and `any` is a TYPE, not a value. That is 11 TypeScript errors in a generated file, which
// nothing catches: `make check` does not type-check the generated output, so the committed
// .ts simply went stale instead and the breakage waited for whoever next ran `make generate`.
//
// Excluding rather than duplicating the literals here is deliberate. Duplicating would
// reintroduce the second definition whose drift caused five broken endpoints, and the UI has
// no use for these — verified: it imports generated-types but references none of these
// eleven names. They are Go-side wire constants shared by the server and the CLI; the browser
// is not a party to them.

// ALIASES. The definitions live in the public configmgr package and this
// references them, because that package is what a consumer compiles against and
// so it is the contract the server conforms to — not the reverse.
//
// The first version of these constants lived here, and it did not work. The CLI
// and the server both read them and agreed; configmgr could not, because it
// imports nothing from internal/ by design — so Push went on sending
// "environment=" to a handler reading "env=" and stayed broken for every
// consumer while the CLI was fixed. A constant in the wrong package is not a
// shared constant: the definition has to live where the outermost caller can
// see it.
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
