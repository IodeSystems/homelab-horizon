package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"homelab-horizon/internal/qr"
	"homelab-horizon/internal/wireguard"
)

// inviteBaseCSS is minimal CSS for the invite page (standalone, not part of the SPA).
const inviteBaseCSS = `
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #1a1a2e; color: #eee; min-height: 100vh; padding: 1rem; }
.card { background: #16213e; border-radius: 8px; padding: 1rem; margin-bottom: 1rem; }
input, button { font-size: 1rem; padding: 0.75rem 1rem; border-radius: 4px; border: none; }
input { background: #0f3460; color: #fff; width: 100%; margin-bottom: 0.5rem; }
input::placeholder { color: #888; }
button { background: #e94560; color: #fff; cursor: pointer; }
button:hover { background: #ff6b6b; }
button.success { background: #2ecc71; }
button.success:hover { background: #27ae60; }
.error { background: #c0392b; padding: 1rem; border-radius: 4px; margin-bottom: 1rem; }
.info { background: #2980b9; padding: 1rem; border-radius: 4px; margin-bottom: 1rem; }
pre { background: #0f3460; padding: 1rem; border-radius: 4px; overflow-x: auto; font-family: monospace; white-space: pre-wrap; word-break: break-all; font-size: 0.85rem; }
a { color: #e94560; }
`

const inviteTemplateHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Get Your VPN Config - Homelab Horizon</title>
    <style>` + inviteBaseCSS + `
    .invite-container { max-width: 500px; margin: 2rem auto; }
    </style>
</head>
<body>
    <div class="invite-container">
        <h1>WireGuard VPN Setup</h1>

        {{if .Error}}<div class="error">{{.Error}}</div>{{end}}

        {{if not .Config}}
        <div class="card">
            <p>Enter a name for your device to generate your VPN configuration.</p>
            <form method="POST" action="/invite/{{.Token}}">
                <input type="text" name="name" placeholder="Device name (e.g., my-laptop)" required autofocus>
                <button type="submit">Generate Config</button>
            </form>
        </div>
        {{else}}
        <div class="card">
            <div class="info">
                <strong>Important:</strong> This is the only time you'll see this configuration. Download it now!
            </div>
            <h2>Scan QR Code (Mobile)</h2>
            <div style="background: white; padding: 1rem; border-radius: 8px; display: inline-block; margin-bottom: 1rem;">
                {{if .QRCode}}{{.QRCode}}{{end}}
            </div>
            <h2>Or Download Config File</h2>
            <pre>{{.Config}}</pre>
            <form method="POST" action="/invite/{{.Token}}/download">
                <input type="hidden" name="config" value="{{.Config}}">
                <input type="hidden" name="name" value="{{.Name}}">
                <button type="submit" class="success">Download .conf File</button>
            </form>
        </div>
        <div class="card">
            <h2>Setup Instructions</h2>
            <ol style="padding-left: 1.5rem; line-height: 1.8;">
                <li>Download the configuration file or scan the QR code</li>
                <li>Install WireGuard on your device:
                    <ul style="padding-left: 1rem; margin: 0.5rem 0;">
                        <li><strong>Windows/Mac:</strong> <a href="https://www.wireguard.com/install/" target="_blank">wireguard.com/install</a></li>
                        <li><strong>Linux:</strong> <code>apt install wireguard</code></li>
                        <li><strong>iOS/Android:</strong> App Store / Play Store</li>
                    </ul>
                </li>
                <li>Import the configuration file into WireGuard</li>
                <li>Connect and enjoy!</li>
            </ol>
        </div>
        {{end}}
    </div>
</body>
</html>`

var inviteTemplateParsed = template.Must(template.New("invite").Parse(inviteTemplateHTML))

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	cfg := r.FormValue("config")
	name := r.FormValue("name")
	if name == "" {
		name = "wireguard"
	}

	filename := strings.ReplaceAll(name, " ", "-") + ".conf"

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write([]byte(cfg))
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/invite/")
	token = strings.TrimSuffix(token, "/download")

	if !s.isValidInvite(token) {
		data := map[string]interface{}{
			"Error": "Invalid or expired invite token",
			"Token": token,
		}
		inviteTemplateParsed.Execute(w, data)
		return
	}

	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/download") {
		s.handleDownload(w, r)
		return
	}

	if r.Method == http.MethodPost {
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			data := map[string]interface{}{
				"Error": "Device name is required",
				"Token": token,
			}
			inviteTemplateParsed.Execute(w, data)
			return
		}

		privKey, pubKey, err := wireguard.GenerateKeyPair()
		if err != nil {
			data := map[string]interface{}{
				"Error": "Failed to generate keys: " + err.Error(),
				"Token": token,
			}
			inviteTemplateParsed.Execute(w, data)
			return
		}

		clientIP, err := s.wg.GetNextIP(s.config.VPNRange)
		if err != nil {
			data := map[string]interface{}{
				"Error": "No available IPs: " + err.Error(),
				"Token": token,
			}
			inviteTemplateParsed.Execute(w, data)
			return
		}

		if err := s.wg.AddPeer(name, pubKey, clientIP); err != nil {
			data := map[string]interface{}{
				"Error": "Failed to add peer: " + err.Error(),
				"Token": token,
			}
			inviteTemplateParsed.Execute(w, data)
			return
		}

		s.wg.Reload()
		s.removeInvite(token)

		clientConfig := wireguard.GenerateClientConfig(
			privKey,
			strings.TrimSuffix(clientIP, "/32"),
			s.config.ServerPublicKey,
			s.config.ServerEndpoint,
			s.config.DNS,
			s.config.GetAllowedIPs(),
		)

		qrCode := qr.GenerateSVG(clientConfig, 256)

		data := map[string]interface{}{
			"Token":  token,
			"Name":   name,
			"Config": clientConfig,
			"QRCode": template.HTML(qrCode),
		}
		inviteTemplateParsed.Execute(w, data)
		return
	}

	data := map[string]interface{}{
		"Token": token,
	}
	inviteTemplateParsed.Execute(w, data)
}
