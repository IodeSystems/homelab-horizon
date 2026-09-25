package server

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// hz DECLARES the static file server as a unit; it does not start one.
//
// THE DECISION (plan/design/privilege-audit.md §7.1 decision 3, the operator,
// 2026-09-25): static serving after the flip is "a separate agent-managed
// unit". hz stops forking a privileged child, the agent declares and manages
// the unit, and the privilege separation the fork provides today is preserved
// by the unit boundary instead.
//
// WHAT THIS FILE IS AND IS NOT. It is the first producer of Desired.Files —
// the generic section, which until now had none. It is hz saying "here is a
// unit, here is the map it reads". It is NOT the flip: the agent is inert
// (item 13 steps 4–5), so nothing writes these files and nothing starts this
// unit. What changes today is that `hz-agent diff` and the drift screen show
// the unit as the thing that ought to be serving, which is what makes the flip
// a visible, reviewable step rather than a silent one.
//
// THE ORDERING THIS LEAVES OPEN, said plainly because it is the one way this
// can go wrong: the moment the agent is armed it will write this unit. If the
// supervisor is still forking a child on the same loopback address, the unit
// and the fork fight over the port. The brake is the Units entry below, which
// carries NO ACTION — the agent places the file and pokes nothing. The commit
// that deletes the supervisor is the commit that gives the unit an action and
// an [Install] section, and it must be the same commit. staticUnitIsInert in
// static_unit_test.go fails if one of those moves without the other.

const (
	// staticUnitName is the unit the agent manages. hz never writes it, never
	// enables it and never starts it — it names it in a payload.
	staticUnitName = "hz-static.service"

	// defaultSystemdUnitDir is where the agent would place it. A default
	// rather than the only answer, because the tests that APPLY hz's payload
	// with the real agent (maintenance_pages_test.go) must not write into the
	// machine running them.
	defaultSystemdUnitDir = "/etc/systemd/system"

	// defaultSiteMapPath is the host->site map when hz cannot say where its
	// own config is. Normally it is a sibling of the config, following
	// <config>.token / <config>.agents / <config>.observed.
	defaultSiteMapPath = "/etc/homelab-horizon/config.json.sites"

	// staticBinaryFallback is the installed location, used when hz cannot say
	// where its own binary is. /usr/local/bin is where install puts it.
	staticBinaryFallback = "/usr/local/bin/homelab-horizon"
)

// staticUnitTemplate is the unit hz declares.
//
// THE PRIVILEGE BOUNDARY, BEFORE AND AFTER. Today hz forks a child and drops
// it to uid 65534; that is the whole of the separation, and the child still
// sees the host's filesystem exactly as nobody would. This unit keeps the uid
// drop AND adds what a fork cannot give itself:
//
//   - ProtectSystem=strict with no ReadWritePaths: every path is read-only.
//     The file server cannot write anything, including the site it serves —
//     which sitedeploy's chown currently makes it the OWNER of.
//   - CapabilityBoundingSet= (empty) and NoNewPrivileges: no capability can be
//     acquired, so the drop cannot be undone.
//   - RestrictAddressFamilies: it can speak IP and nothing else. No unix
//     socket to hz, no netlink.
//   - A syscall filter, PrivateDevices, PrivateTmp, and the kernel-surface
//     restrictions.
//
// Placeholders are __UPPER__ rather than printf verbs, matching hz-agent's and
// hz-probe's installers: a unit file can contain %-escapes of its own.
//
// NO [Install] SECTION, deliberately, for the reason hz-agent's unit has none:
// `systemctl enable` refuses without one, so the unit cannot be armed by
// autocomplete. Both that and the empty Units action below come off in the
// commit that retires the supervisor.
const staticUnitTemplate = `[Unit]
Description=hz-static - the unprivileged static file server for homelab-horizon
Documentation=https://github.com/iodesystems/homelab-horizon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
# Static-file-server mode is selected by the environment below, not by an
# argument: the binary checks HZ_STATIC_SERVER_ADDR before it parses flags.
ExecStart=__EXEC__
Restart=on-failure
RestartSec=5

Environment=HZ_STATIC_SERVER_ADDR=__ADDR__
Environment=HZ_STATIC_SITES=__SITES__

# The privilege separation. hz used to get this by forking and dropping the
# child to nobody. It comes from here now, and the binary above must be
# readable and executable by this user — a binary under a mode-0750 home
# directory is the measured way that fails (privilege-audit.md §1.5). Under a
# unit that failure is a failed unit in systemctl status, not a silent respawn
# loop.
User=nobody

NoNewPrivileges=true
CapabilityBoundingSet=
AmbientCapabilities=

# Read-only everything. There are no ReadWritePaths on purpose: a file server
# writes nothing, and the site it serves must not be one of the things it can
# change.
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ProtectProc=invisible
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_INET AF_INET6
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
UMask=0077

# There is deliberately no [Install] section, so "systemctl enable hz-static"
# has nothing to hook onto and refuses. hz still forks its own static child;
# two servers on one loopback port is how the sites go down. See
# plan/design/privilege-audit.md §7.A — the commit that deletes the supervisor
# is the commit that adds this section.
`

// staticPaths is where the two declared files go. Computed from the Server
// (Server.staticPaths) rather than baked in, for the same reason
// HAProxyErrorsDir is a method: one definition of the path, and a test can
// point it somewhere that is not the machine's /etc.
type staticPaths struct {
	SiteMap string
	Unit    string
}

// renderStaticUnit fills the template in.
func renderStaticUnit(exe, addr, sitesPath string) string {
	return strings.NewReplacer(
		"__EXEC__", exe,
		"__ADDR__", addr,
		"__SITES__", sitesPath,
	).Replace(staticUnitTemplate)
}

// staticFilesSection is hz's declaration of the static file server: the unit,
// the map it reads, and the unit's name for the agent to poke.
//
// ALWAYS A SECTION FOR THE LOCAL BOX, AND THE MAP IS WHERE "NOTHING HERE"
// GETS SAID. A machine with no static services declared gets a unit and a map
// holding `{}` — not a missing section. The two are different states and the
// difference is load-bearing in both directions:
//
//   - nil means hz does not manage static serving on this machine at all, and
//     that is the honest answer for a machine whose service list hz cannot
//     see (desiredFor's remote branch, gapped in noteRemoteGaps).
//   - `{}` means hz looked at its own service list and there are no static
//     roots. The server binds and answers 404, which is what "no sites" looks
//     like from outside.
//
// The child already tells those apart on its side: an empty map serves, an
// unreadable one refuses to bind (RunStaticServerChild). Declaring the unit
// unconditionally is also what keeps the flip one shape instead of two, at
// the cost of an idle unit on a gateway with no static sites — cheap, unlike
// the fork it replaces, which retried a doomed exec forever (§1.5).
func staticFilesSection(cfg *config.Config, exe string, p staticPaths) *agent.FilesSection {
	if cfg == nil {
		return nil
	}
	if exe == "" {
		exe = staticBinaryFallback
	}
	if p.SiteMap == "" {
		p.SiteMap = defaultSiteMapPath
	}
	if p.Unit == "" {
		p.Unit = defaultSystemdUnitDir + "/" + staticUnitName
	}

	// One deriver, two transports. The supervisor pushes this same map down a
	// pipe; a second definition of "which host serves which root" is how the
	// two would come to disagree.
	sites := deriveStaticSites(cfg)
	b, err := json.MarshalIndent(sites, "", "  ")
	if err != nil {
		// Unreachable for this type. A map hz cannot marshal must not cross as
		// an empty one — the child would read that as "no sites here".
		return nil
	}

	return &agent.FilesSection{
		Files: []agent.File{
			{Path: p.SiteMap, Mode: 0o644, Contents: string(b) + "\n"},
			{Path: p.Unit, Mode: 0o644, Contents: renderStaticUnit(exe, cfg.StaticServeAddr(), p.SiteMap)},
		},
		Units: []agent.Unit{{
			// NO ACTION, and that word is the whole brake. agent.Unit's doc:
			// "Empty pokes nothing". The unit is declared and placed; starting
			// it belongs to the commit that stops hz forking its own child.
			Name: staticUnitName,
		}},
	}
}

// staticPaths is where this hz says the two declared files go.
//
// The site map is a SIBLING OF THE CONFIG, following <config>.token,
// <config>.agents and <config>.observed — one directory holds hz's state, and
// hz's unit already creates it. Unlike those three it is 0644 and not secret:
// the unit's unprivileged user has to read it.
func (s *Server) staticPaths() staticPaths {
	dir := s.unitDir
	if dir == "" {
		dir = defaultSystemdUnitDir
	}
	siteMap := defaultSiteMapPath
	if s.configPath != "" {
		siteMap = s.configPath + ".sites"
	}
	return staticPaths{SiteMap: siteMap, Unit: filepath.Join(dir, staticUnitName)}
}
