package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/hzlog"
)

// The cobra command tree (CLI-1).
//
// hz was a set of boolean mode flags on one flag.FlagSet: `-install`, `-check`,
// `-config-template` and so on, each selecting a branch of a switch. Canon is
// cobra, and the modes really are subcommands — they share almost nothing but
// the binary.
//
// Serving is the ROOT's own action rather than `hz serve`, because the systemd
// unit installed on every existing box runs the binary with no arguments and a
// tree that printed help instead of serving would take those boxes down on
// upgrade.

// serveOpts carries what the root command needs to start the server. Named
// rather than a pile of closure variables so the flags and their uses stay next
// to each other.
type serveOpts struct {
	configPath       string
	dryRun           bool
	noMCP            bool
	enableAdminToken bool
	listenAddr       string
}

func newRoot() *cobra.Command {
	var opts serveOpts

	root := &cobra.Command{
		Use:   "homelab-horizon",
		Short: "Homelab edge gateway — WireGuard, DNS, HAProxy, certificates",
		Long: "homelab-horizon (hz) is the edge gateway: WireGuard VPN, dnsmasq, HAProxy,\n" +
			"Let's Encrypt and blue-green deploys, administered over HTTP or the hz CLI.\n\n" +
			"Run with no arguments to serve, which is what the systemd unit does.",
		SilenceUsage: true,
		// A failed serve should print the error, not a wall of usage: the
		// problem is almost never the command line.
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			runServer(opts.configPath, opts.dryRun, !opts.noMCP, opts.enableAdminToken, opts.listenAddr)
			return nil
		},
	}

	// Persistent, because -config and -dry-run applied to every old mode flag
	// and scripts pass them in any order.
	root.PersistentFlags().StringVar(&opts.configPath, "config", "",
		"path to the configuration file (optional)")
	root.PersistentFlags().BoolVar(&opts.dryRun, "dry-run", false,
		"show what would be done without changing anything")

	root.Flags().BoolVar(&opts.noMCP, "no-mcp", false,
		"disable the MCP tool server (enabled over stdio by default)")
	root.Flags().BoolVar(&opts.enableAdminToken, "enable-admin-token", false,
		"re-enable the shared admin token, then serve (console recovery)")
	root.Flags().StringVar(&opts.listenAddr, "listen", "",
		"override the listen address for this run only, e.g. 127.0.0.1:8080 "+
			"(PCI DSS 2.2.7: reachable only via the HTTPS vhost). Not persisted — "+
			"restart without it to revert")

	root.AddCommand(
		newVersionCmd(),
		newInstallCmd(&opts),
		newCheckCmd(&opts),
		newConfigTemplateCmd(),
		newIAMPolicyCmd(),
		newShowSystemdCmd(&opts),
		newTokenCmd(&opts),
		newUserCmd(&opts),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and exit",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("homelab-horizon %s (built %s)\n", Version, BuildTime)
		},
	}
}

func newInstallCmd(opts *serveOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the systemd service",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return installService(opts.dryRun)
		},
	}
}

func newCheckCmd(opts *serveOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Check system configuration and offer to fix what is wrong",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCheck(opts.dryRun)
		},
	}
}

func newConfigTemplateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config-template",
		Short: "Print a commented configuration template and exit",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Print(config.Template())
		},
	}
}

func newIAMPolicyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "iam-policy",
		Short: "Print an IAM policy template for Route53 access",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Print(config.IAMPolicyTemplate())
		},
	}
}

func newShowSystemdCmd(opts *serveOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "show-systemd",
		Short: "Print the systemd unit that would be generated",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println(generateSystemdService(opts.configPath))
		},
	}
}

// legacyFlags maps the single-dash mode flags hz used to take onto the
// subcommand that replaced each one.
//
// pflag reads `-enable-admin-token` as a cluster of shorthand flags and fails,
// so migrating to cobra without this would break every documented invocation —
// including `homelab-horizon -enable-admin-token`, which is the console recovery
// in docs/mfa-lockout-recovery.md and may well be read off a printout by someone
// who has no other way into the box. Breaking that to satisfy a lint rule would
// be the wrong trade.
var legacyFlags = map[string]string{
	"-install":         "install",
	"-check":           "check",
	"-config-template": "config-template",
	"-iam-policy":      "iam-policy",
	"-show-systemd":    "show-systemd",
	"-version":         "version",
}

// legacyValueFlags are the single-dash forms that keep a value or stay a flag on
// the root rather than becoming a subcommand.
var legacyValueFlags = map[string]bool{
	"-listen":             true,
	"-config":             true,
	"-dry-run":            true,
	"-no-mcp":             true,
	"-enable-admin-token": true,
}

// translateLegacyArgs rewrites old-style arguments into the cobra tree's shape.
//
// Deliberately a translation at the edge rather than aliases inside cobra: it
// keeps the compatibility surface in one readable list that can be deleted
// wholesale once nothing invokes the old forms, instead of scattering
// deprecated spellings through the command definitions.
func translateLegacyArgs(args []string) (out []string, translated bool) {
	subcommands := make([]string, 0, 1)
	rest := make([]string, 0, len(args))

	for _, arg := range args {
		name, value, hasValue := strings.Cut(arg, "=")
		switch {
		case legacyFlags[name] != "":
			subcommands = append(subcommands, legacyFlags[name])
			translated = true
		case legacyValueFlags[name]:
			rewritten := "-" + name // -config -> --config
			if hasValue {
				rewritten += "=" + value
			}
			rest = append(rest, rewritten)
			translated = true
		default:
			rest = append(rest, arg)
		}
	}

	// A subcommand has to come first, and only one can win. Two mode flags at
	// once was ambiguous under the old switch too — it picked by case order —
	// so take the first rather than inventing a precedence nobody relied on.
	if len(subcommands) > 0 {
		return append(subcommands[:1], rest...), true
	}
	return rest, translated
}

// runCLI parses arguments and dispatches. Split from main so the legacy
// translation is testable without exec'ing the binary.
func runCLI(args []string) error {
	translated, changed := translateLegacyArgs(args)
	if changed {
		// stderr, and only when something was rewritten: the message is for a
		// human running the old form by hand, and hz's stdout is parsed by
		// scripts (config-template, show-systemd).
		slog.Warn("single-dash flags are the pre-cobra spelling and still work; " +
			"prefer subcommands and double dashes")
	}

	root := newRoot()
	root.SetArgs(translated)
	return root.Execute()
}

// exitWith prints an error the way the old switch did — a slog line, then a
// non-zero exit — so failure output in journald does not change shape.
func exitWith(err error) {
	if err == nil {
		return
	}
	slog.Error("command failed", "err", err)
	os.Exit(1)
}

// ensureLogging is called before dispatch so subcommands that print to stdout
// still get their diagnostics on stderr.
func ensureLogging() {
	hzlog.Setup()
}
