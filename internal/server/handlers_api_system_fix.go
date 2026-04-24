package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/config"
	"homelab-horizon/internal/wireguard"
)

// Fixer endpoints for on-host system state. These restore the "fix this"
// buttons that lived on the old Go-template setup page (ripped in 807364b).
// Every endpoint is POST + admin-only; responses are JSON {ok:true} on
// success and {error:"..."} on failure.

func (s *Server) writeFixOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})
}

// systemdRun executes a command inside a transient one-shot unit so it escapes
// horizon's own ProtectSystem=strict sandbox. Mirrors the pattern the ripped
// handlers used. The command's combined stdout/stderr is returned for errors.
func systemdRun(args ...string) (string, error) {
	cmd := exec.Command("systemd-run", append([]string{"--pipe", "--wait", "--service-type=oneshot"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), err
	}
	return strings.TrimSpace(string(out)), nil
}

// POST /api/v1/system/fix/ip-forwarding — sysctl net.ipv4.ip_forward=1
func (s *Server) handleAPISystemFixIPForwarding(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if err := wireguard.EnableIPForwarding(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/fix/masquerade — iptables POSTROUTING -j MASQUERADE for
// the current default-route interface. Idempotent: if the rule already exists
// the command errors, but we check first via CheckSystem and skip adding.
func (s *Server) handleAPISystemFixMasquerade(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if err := wireguard.AddMasqueradeRule(s.cfg().VPNRange); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/fix/wg-forward-chain — (re)install the WG-FORWARD chain
// with per-peer profile rules based on current config + peers + LAN CIDR.
func (s *Server) handleAPISystemFixWGForwardChain(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	cfg := s.cfg()
	lanCIDR := config.GetLocalNetworkCIDR(config.DetectDefaultInterface())
	peers := s.wg.GetPeers()
	if err := wireguard.SetupForwardChain(cfg.WGInterface, peers, cfg.VPNProfiles, cfg.VPNRange, lanCIDR); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/fix/wg-rules — regenerate PostUp/PostDown in wg0.conf
// for the current default-route iface, then bounce the interface so new rules
// take effect.
func (s *Server) handleAPISystemFixWGRules(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	outIface := config.DetectDefaultInterface()
	if outIface == "" {
		outIface = "eth0"
	}
	postUp := wireguard.ExpectedPostUp(outIface)
	postDown := wireguard.ExpectedPostDown(outIface)
	if err := s.wg.UpdateInterfaceRules(postUp, postDown); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "update rules: "+err.Error())
		return
	}
	if err := s.wg.InterfaceDown(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "restart down: "+err.Error())
		return
	}
	if err := s.wg.InterfaceUp(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "restart up: "+err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/wg/create-config — generate a fresh [Interface] wg0.conf with
// a new keypair + PostUp/PostDown for the current default iface. Only used
// on a truly fresh install; persists the server's public key into config.
func (s *Server) handleAPIWGCreateConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	cfg := s.cfg()

	privKey, pubKey, err := wireguard.GenerateKeyPair()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "keygen: "+err.Error())
		return
	}

	serverIP := strings.Split(cfg.VPNRange, "/")[0]
	serverIP = strings.TrimSuffix(serverIP, ".0") + ".1"

	outIface := config.DetectDefaultInterface()
	if outIface == "" {
		outIface = "eth0"
	}

	wgConfig := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/24
ListenPort = 51820
PostUp = %s
PostDown = %s
`, privKey, serverIP, wireguard.ExpectedPostUp(outIface), wireguard.ExpectedPostDown(outIface))

	// /etc is sandboxed from horizon's own systemd unit — shell out through
	// systemd-run to escape ProtectSystem=strict.
	script := fmt.Sprintf("mkdir -p /etc/wireguard && chmod 700 /etc/wireguard && cat > %s && chmod 600 %s",
		cfg.WGConfigPath, cfg.WGConfigPath)
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot", "bash", "-c", script)
	cmd.Stdin = strings.NewReader(wgConfig)
	if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "write config: "+cmdErr.Error()+" — "+strings.TrimSpace(string(out)))
		return
	}

	if err := s.updateConfig(func(c *config.Config) { c.ServerPublicKey = pubKey }); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save public key: "+err.Error())
		return
	}
	_ = s.wg.Load()

	s.writeFixOK(w)
}

// POST /api/v1/system/install/horizon-unit — writes the systemd unit that
// supervises horizon itself into /etc/systemd/system/homelab-horizon.service
// and runs daemon-reload so the new unit is visible.
func (s *Server) handleAPISystemInstallHorizonUnit(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	binaryPath := "/usr/local/bin/homelab-horizon"
	if execPath, err := os.Executable(); err == nil {
		if abs, err := filepath.Abs(execPath); err == nil {
			binaryPath = abs
		}
	}
	configPath := s.configPath
	if abs, err := filepath.Abs(configPath); err == nil {
		configPath = abs
	}
	content := config.GenerateServiceFile(binaryPath, configPath)

	const servicePath = "/etc/systemd/system/homelab-horizon.service"
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"bash", "-c", fmt.Sprintf("cat > %s", servicePath))
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "write unit: "+err.Error()+" — "+strings.TrimSpace(string(out)))
		return
	}
	if out, err := systemdRun("systemctl", "daemon-reload"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "daemon-reload: "+err.Error()+" — "+out)
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/system/enable/horizon — systemctl enable homelab-horizon
func (s *Server) handleAPISystemEnableHorizon(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}
	if out, err := systemdRun("systemctl", "enable", "homelab-horizon"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error()+" — "+out)
		return
	}
	s.writeFixOK(w)
}
