package server

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The PCI tab's data.
//
// State comes from hzControls — the same function the metrics exporter uses —
// so the tab and Prometheus can never disagree about whether a control is met.
// What is added here is everything a person needs and a gauge does not: what
// the control is called in English, why it is unmet right now, and whether hz
// can do anything about it.
//
// Remediation is deliberately advice rather than an action. The server says
// which tier a control is in and what the risk is; the client calls the same
// endpoints an operator would use by hand. A generic "apply this fix" executor
// would have to re-implement the validation those endpoints already do — the
// admin-token disable refusing to strand the last way in, the MFA scope change
// refusing to jail admins with no second factor — and the first time the two
// drifted, the tab would be the one that was wrong.

// remediationKind is how much judgment a fix needs.
type remediationKind string

const (
	// remediationFix is safe to apply from a button. It cannot log anyone out
	// or make the box unreachable.
	remediationFix remediationKind = "fix"
	// remediationDecision can lock somebody out. The client must explain the
	// risk and make the operator confirm; the warning text says what to check.
	remediationDecision remediationKind = "decision"
	// remediationManual is not hz's to apply — it needs a shell on the host,
	// or a judgment about a vhost hz cannot see.
	remediationManual remediationKind = "manual"
)

// PCIControl is one row of the checklist.
type PCIControl struct {
	Name        string `json:"name"`
	Requirement string `json:"requirement"`
	Title       string `json:"title"`
	OK          bool   `json:"ok"`
	// Wants is what the standard asks for, in a sentence.
	Wants string `json:"wants"`
	// Detail is why this control reads as it does right now. Empty when met.
	Detail string `json:"detail,omitempty"`
	// Remediation is absent when the control is already met.
	Remediation *PCIRemediation `json:"remediation,omitempty"`
}

// PCIRemediation is what can be done about an unmet control.
type PCIRemediation struct {
	Kind remediationKind `json:"kind"`
	// Label is the button text, or for a manual control, empty.
	Label string `json:"label,omitempty"`
	// Warning is what the operator is agreeing to. Required for "decision".
	Warning string `json:"warning,omitempty"`
	// Hint is where to go when hz will not do it for you.
	Hint string `json:"hint,omitempty"`
}

// pciMeta is the fixed copy for a control: everything that does not depend on
// the current configuration.
type pciMeta struct {
	title string
	wants string
	kind  remediationKind
	label string
	warn  string
	hint  string
}

// pciCatalogue covers every control hzControls emits. A control with no entry
// here would render as a blank row, so TestPCICatalogueCoversEveryControl
// fails the build instead.
var pciCatalogue = map[string]pciMeta{
	"vpn_mfa_enabled": {
		title: "Second factor on the VPN",
		wants: "Access into the network needs more than a key file.",
		kind:  remediationManual,
		hint: "Settings › VPN MFA — enabling this jails peers until they " +
			"authenticate at the portal, so enrol a second factor for yourself " +
			"first. The portal is reachable from inside the jail, but only if " +
			"HAProxy is serving it.",
	},
	"vpn_mfa_no_admin_bypass": {
		title: "No standing bypass for admins",
		wants: "Administrators are not exempt from the second factor.",
		kind:  remediationManual,
		hint: "Settings › VPN MFA — set the scope to all peers. You lose your " +
			"own bypass; hz refuses while any VPN admin has no second factor " +
			"enrolled, and names them.",
	},
	"vpn_mfa_session_bounded": {
		title: "Idle VPN sessions expire",
		wants: "A VPN session stops working after 15 minutes without traffic.",
		kind:  remediationManual,
		hint: "Settings › VPN MFA — set the inactivity timeout to 15 minutes or " +
			"less (hz will not go below 5, because the traffic counters behind it " +
			"are sampled once a minute). Idleness is measured from bytes moved, " +
			"not from the tunnel being up: keepalives keep a tunnel handshaking " +
			"whether or not anyone is using it. How long a session may last is a " +
			"separate setting and does not affect this — the requirement is about " +
			"idleness, not duration.",
	},
	"tls_enabled": {
		title: "TLS on published services",
		wants: "Traffic to services hz publishes is encrypted.",
		kind:  remediationManual,
		hint: "Enable Let's Encrypt on the System tab and issue certificates — " +
			"a certificate has to exist before the floor below means anything.",
	},
	"tls_min_version": {
		title: "TLS 1.2 or better",
		wants: "TLS 1.0 and 1.1 are refused.",
		kind:  remediationManual,
		hint:  "Set the TLS floor on the System tab.",
	},
	"time_synchronised": {
		title: "Synchronised clock",
		wants: "The host's clock is disciplined by NTP.",
		kind:  remediationManual,
		hint: "On the host: timedatectl set-ntp true. TOTP silently depends on " +
			"this too — a drifting clock rejects correct codes.",
	},
	"no_shared_admin_token": {
		title: "No shared admin token",
		wants: "Every administrator is individually identified.",
		kind:  remediationDecision,
		label: "Disable the shared token…",
		warn: "Anything authenticating with the token stops working — scripts, " +
			"the hz CLI, bookmarks. Recovery is at the console: run " +
			"homelab-horizon --enable-admin-token. Check what still uses it first.",
		hint: "Accounts and VPN admin peers remain as ways in.",
	},
	"patches_current": {
		title: "Security updates installed",
		wants: "No outstanding security updates on the host.",
		kind:  remediationManual,
		hint:  "On the host: apt-get update && apt-get upgrade.",
	},
	"session_idle_timeout": {
		title: "Idle sessions expire",
		wants: "An idle admin session stops working within 15 minutes.",
		kind:  remediationDecision,
		label: "Set 15 minutes…",
		warn: "This signs you out too, after 15 minutes without a request. " +
			"Your own session restarts the clock on every request.",
		hint: "Settings › Users › Account policy",
	},
	"login_lockout": {
		title: "Lockout after failed attempts",
		wants: "An account locks for 30 minutes after at most 10 failures.",
		kind:  remediationFix,
		label: "Set 10 attempts / 30 minutes",
		hint:  "Only ever acts on someone already failing to sign in.",
	},
	"password_history": {
		title: "No password reuse",
		wants: "The last four passwords cannot be reused.",
		kind:  remediationFix,
		label: "Remember 4 passwords",
	},
	"password_rotation": {
		title: "Password rotation",
		wants: "Passwords older than 90 days must be changed.",
		kind:  remediationDecision,
		label: "Expire after 90 days…",
		warn: "Accounts whose password is older than this must change it at the " +
			"next sign-in. Accounts holding a second factor are exempt, which is " +
			"what the requirement itself allows.",
	},
	"log_persistence": {
		title: "Audit history kept",
		wants: "Twelve months of logs, surviving reboots.",
		kind:  remediationFix,
		label: "Keep 12 months",
		hint:  "Writes a journald drop-in and restarts the journal.",
	},
	"admin_access_encrypted": {
		title: "Admin access encrypted",
		wants: "The admin interface is not reachable in cleartext off this host.",
		kind:  remediationManual,
		hint: "No button on purpose: rebinding the listener can cut off whoever " +
			"is reading this, and the safe route depends on your HTTPS vhost. " +
			"Try it as a start option first — restart hz with --listen " +
			"127.0.0.1:8080, which reverts on the next restart — then set " +
			"listen_addr once the vhost is confirmed.",
	},
}

// pciControls joins live state with the catalogue.
func (s *Server) pciControls() []PCIControl {
	cfg := s.cfg()
	facts := s.hostFacts.snapshot()

	out := make([]PCIControl, 0, len(pciCatalogue))
	for _, ctl := range hzControls(cfg, facts) {
		meta, known := pciCatalogue[ctl.name]
		if !known {
			// Report it rather than dropping it: an unnamed control is still a
			// finding, and a silent omission would understate the checklist.
			meta = pciMeta{title: ctl.name, kind: remediationManual}
		}

		row := PCIControl{
			Name:        ctl.name,
			Requirement: ctl.requirement,
			Title:       meta.title,
			OK:          ctl.ok,
			Wants:       meta.wants,
		}
		if !ctl.ok {
			row.Detail = pciDetail(ctl.name, cfg, facts)
			row.Remediation = &PCIRemediation{
				Kind:    meta.kind,
				Label:   meta.label,
				Warning: meta.warn,
				Hint:    meta.hint,
			}
		}
		out = append(out, row)
	}

	// Unmet first, then by requirement, so the checklist opens on the work
	// rather than on a wall of green.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OK != out[j].OK {
			return !out[i].OK
		}
		return out[i].Requirement < out[j].Requirement
	})
	return out
}

// pciDetail says why a control reads unmet, using the actual numbers. Generic
// copy ("not configured") is what makes a compliance screen useless.
func pciDetail(name string, cfg *config.Config, facts hostFactsSnapshot) string {
	switch name {
	case "vpn_mfa_enabled":
		return "VPN MFA is off."
	case "vpn_mfa_no_admin_bypass":
		if !cfg.VPNMFAEnabled {
			return "VPN MFA is off, so the question of a bypass does not arise yet."
		}
		return fmt.Sprintf("Enforcement scope is %q; admins are exempt.", cfg.MFAScope())
	case "vpn_mfa_session_bounded":
		if !cfg.VPNMFAEnabled {
			return "VPN MFA is off."
		}
		if cfg.MFAInactivityTimeout() == 0 {
			return "No inactivity timeout is set, so a session survives any amount of silence."
		}
		return fmt.Sprintf("Sessions go stale after %d minutes idle; the standard wants 15 or less.",
			int(cfg.MFAInactivityTimeout().Minutes()))
	case "tls_enabled":
		return "Let's Encrypt is not enabled, so hz publishes no certificates."
	case "tls_min_version":
		if !cfg.SSLEnabled {
			return "TLS is off, so there is no floor to enforce."
		}
		return "The configured TLS floor still permits 1.0 or 1.1."
	case "time_synchronised":
		if !facts.measured {
			return "Not measured yet."
		}
		return "The host clock is not disciplined by NTP."
	case "no_shared_admin_token":
		return "The shared admin token is enabled, so actions taken with it " +
			"are attributable only to whoever holds it."
	case "patches_current":
		if !facts.measured {
			return "Not measured yet."
		}
		return fmt.Sprintf("%d security updates are outstanding.", facts.securityUpdates)
	case "session_idle_timeout":
		if cfg.Policy.IdleMinutes <= 0 {
			return "No idle timeout is set."
		}
		return fmt.Sprintf("Idle timeout is %d minutes; the standard wants 15 or less.",
			cfg.Policy.IdleMinutes)
	case "login_lockout":
		return fmt.Sprintf("Locks after %d attempts for %d minutes.",
			cfg.Policy.EffectiveMaxFailedAttempts(), cfg.Policy.EffectiveLockoutMinutes())
	case "password_history":
		return fmt.Sprintf("%d previous passwords are remembered; the standard wants 4.",
			cfg.Policy.EffectivePasswordHistory())
	case "password_rotation":
		return "Passwords do not expire."
	case "log_persistence":
		switch {
		case !facts.measured:
			return "Not measured yet."
		case !facts.journalPersistent:
			return "The journal is volatile: every log is lost at the next reboot."
		case facts.journalRetention == 0:
			return "No retention limit is set, so logs rotate on size alone."
		default:
			return fmt.Sprintf("Logs are kept for %d days; the standard wants 365.",
				int(facts.journalRetention.Hours()/24))
		}
	case "admin_access_encrypted":
		return fmt.Sprintf("The admin interface listens on %s.", cfg.EffectiveListenAddr())
	}
	return ""
}

// GET /api/v1/pci/controls
func (s *Server) handleAPIPCIControls(w http.ResponseWriter, r *http.Request) {
	// Admin only. This response is a list of exactly which hardening measures
	// are not in place on this gateway, which is reconnaissance for anyone who
	// can reach the port.
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	controls := s.pciControls()
	unmet := 0
	for _, c := range controls {
		if !c.OK {
			unmet++
		}
	}
	writeJSON(w, map[string]any{
		"controls": controls,
		"unmet":    unmet,
		// Said once, here, so every surface that renders this repeats it: hz
		// reports how it is configured, not whether an assessor agrees.
		"disclaimer": "Describes how hz is configured. It is not an assertion of compliance.",
	})
}
