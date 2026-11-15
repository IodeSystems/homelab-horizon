package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/wgkey"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// `hz-agent wg-create-config` — where POST /api/v1/wg/create-config went.
//
// THE DECISION (operator, 2026-09-25): "hz-agent owns it all", and this file is
// one the agent will own anyway — wg0.conf already crosses the hz→agent channel
// as Secret material (CLAUDE.md §3's qualification). The bootstrap objection
// against moving it — "the agent may not be on the box yet" — was dissolved the
// same day by making every `homelab-horizon install` place the agent binary
// (cmd/homelab-horizon/agentbin.go), so the verb lands on a binary that is
// always present before enrolment.
//
// THE HANDLER IT REPLACES built its file by piping an hz-built shell string
// into `systemd-run … bash -c "mkdir -p … && cat > … && chmod 600 …"`, to
// escape hz's own ProtectSystem=strict sandbox. Here there is no sandbox to
// escape and no shell: os.MkdirAll, os.WriteFile, done.
//
// IT REFUSES TO OVERWRITE, and that is the feature, not a safety rail
// (privilege-audit.md §3.1 #5 says so explicitly). wg0.conf carries the
// gateway's server private key. Regenerating it mints a new server identity,
// which silently invalidates every client config ever handed out — every phone,
// every laptop, every site-to-site peer — and the tunnel the operator is
// probably running this over goes with them. "The file is already there" is the
// only signal that would have warned anyone, so it is a hard refusal with no
// --force: moving the old file aside is a deliberate act that leaves a backup,
// and a flag is not.
//
// ADDING THIS VERB DOES NOT ARM THE AGENT. It writes when a human runs it and
// never otherwise: nothing in `run`, `diff` or the unit reaches it, and the
// unit is still unenableable and still carries no --apply (install_test.go).

const wgListenPort = 51820

// wgCreateConfigEnv seams the verb's edges so the test drives real files in a
// temp directory without needing root or a `wg` binary.
type wgCreateConfigEnv struct {
	// loadConfig yields hz's config: the VPN range to address the interface
	// from, and where wg0.conf belongs.
	loadConfig func() (*config.Config, error)
	// defaultIface is the machine read — which interface the default route
	// leaves by, for the MASQUERADE line.
	defaultIface func() string
	// mintKey produces the server's key pair.
	mintKey func() (private, public string, err error)
	euid    int
}

func realWGCreateConfigEnv(configPath string) wgCreateConfigEnv {
	return wgCreateConfigEnv{
		loadConfig: func() (*config.Config, error) {
			if configPath != "" {
				return config.Load(configPath)
			}
			cfg, _, err := config.LoadAuto()
			return cfg, err
		},
		defaultIface: config.DetectDefaultInterface,
		// wgkey mints in-process (crypto/ecdh), which is what internal/agent
		// already uses for this box's segment keys. Deliberately NOT
		// wireguard.GenerateKeyPair, which shells to `wg genkey`: this verb runs
		// at bootstrap, and a gateway that has not installed wireguard-tools yet
		// is exactly the box that needs it.
		mintKey: wgkey.Generate,
		euid:    os.Geteuid(),
	}
}

func runWGCreateConfig(args []string) error {
	fset := flag.NewFlagSet("wg-create-config", flag.ExitOnError)
	configPath := fset.String("hz-config", "", "hz's configuration file (default: the standard search paths)")
	dryRun := fset.Bool("dry-run", false, "say what would be written, write nothing")
	if err := fset.Parse(args); err != nil {
		return err
	}
	return realWGCreateConfigEnv(*configPath).create(os.Stdout, *dryRun)
}

func (e wgCreateConfigEnv) create(out io.Writer, dryRun bool) error {
	cfg, err := e.loadConfig()
	if err != nil {
		return fmt.Errorf("reading hz's configuration: %w", err)
	}
	path := cfg.WGConfigPath
	if path == "" {
		return errors.New("hz's configuration names no wg_config_path")
	}

	// THE REFUSAL, and it comes first — before the key is minted, before root
	// is checked, before anything is read that could fail for another reason.
	// A refusal that ran second would still be correct and would still have
	// generated a key pair nobody can use.
	switch _, statErr := os.Stat(path); {
	case statErr == nil:
		return fmt.Errorf("%s already exists — refusing to replace it.\n"+
			"  It holds this gateway's server private key. Writing a new one mints a new\n"+
			"  server identity, and every client config ever handed out — every phone, every\n"+
			"  laptop, every site-to-site peer — stops working at once, including the tunnel\n"+
			"  you may be reading this over.\n"+
			"  If that is genuinely what you want, move the file aside yourself:\n"+
			"    sudo mv %s %s.old\n"+
			"  and run this again. There is no --force, on purpose: the backup is the point",
			path, path, path)
	case !errors.Is(statErr, fs.ErrNotExist):
		// Unreadable is not absent. A verb that treated "I could not tell" as
		// "there is no file" would overwrite exactly the config it must not.
		return fmt.Errorf("cannot tell whether %s already exists: %w — refusing to write", path, statErr)
	}

	address, err := wireguard.ServerAddress(cfg.VPNRange)
	if err != nil {
		return fmt.Errorf("deriving this gateway's VPN address: %w", err)
	}

	iface := e.defaultIface()
	if iface == "" {
		iface = "eth0"
	}

	dir := filepath.Dir(path)
	if dryRun {
		_, _ = fmt.Fprintf(out, "DRY RUN: would write %s (mode 0600) in %s (mode 0700).\n", path, dir)
		_, _ = fmt.Fprintf(out, "  Address    %s\n", address)
		_, _ = fmt.Fprintf(out, "  ListenPort %d\n", wgListenPort)
		_, _ = fmt.Fprintf(out, "  NAT out of %s\n", iface)
		_, _ = fmt.Fprintln(out, "  A new server key pair would be minted. Nothing was written.")
		return nil
	}

	if e.euid != 0 {
		return fmt.Errorf("must run as root to write %s (try: sudo hz-agent wg-create-config)", path)
	}

	private, public, err := e.mintKey()
	if err != nil {
		return fmt.Errorf("minting the server key pair: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	body := wireguard.RenderServerInterface(wireguard.ServerInterface{
		PrivateKey: private,
		Address:    address,
		ListenPort: wgListenPort,
		PostUp:     wireguard.ExpectedPostUp(iface),
		PostDown:   wireguard.ExpectedPostDown(iface),
	})
	if err := writeNewFile(path, body); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "Wrote %s (mode 0600).\n", path)
	_, _ = fmt.Fprintf(out, "  Address    %s\n", address)
	_, _ = fmt.Fprintf(out, "  ListenPort %d\n", wgListenPort)
	_, _ = fmt.Fprintf(out, "  NAT out of %s\n", iface)
	_, _ = fmt.Fprintf(out, "  Server public key: %s\n", public)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "The private key stays in that file and is not printed. hz reads the public")
	_, _ = fmt.Fprintln(out, "half out of it at startup when its own record is empty, so:")
	_, _ = fmt.Fprintln(out, "  sudo systemctl restart homelab-horizon   # picks up the server public key")
	_, _ = fmt.Fprintln(out, "  sudo wg-quick up wg0                     # brings the interface up")
	return nil
}

// writeNewFile writes body to a path that must not exist.
//
// A SEPARATE FUNCTION, and O_EXCL rather than a plain create, because the stat
// above is a MESSAGE and this is the guarantee. The stat can say why it is
// refusing and what to do instead; it cannot promise anything, because between
// the stat and the write is a window. Only the kernel can close that, and it is
// what actually stops a second run replacing the gateway's server identity.
//
// Split out so the guarantee can be tested on its own. Reached through create()
// it is unreachable — the stat refuses first — so a test that drove only
// create() would pass with O_EXCL removed, which is a test pinned where the
// property is already enforced by something else.
func writeNewFile(path, body string) error {
	fh, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := fh.WriteString(body); err != nil {
		_ = fh.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
