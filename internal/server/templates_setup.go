package server

const setupTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>Setup - Homelab Horizon</title>
    <style>` + baseCSS + `</style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>Setup & Configuration</h1>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        {{if .Message}}<div class="success">{{.Message}}</div>{{end}}
        {{if .Error}}<div class="error">{{.Error}}</div>{{end}}

        {{if .SetupComplete}}
        <div class="card" style="text-align: center; background: linear-gradient(135deg, #0f3460 0%, #16213e 100%); border-left: 4px solid #2ecc71;">
            <h2 style="color: #2ecc71;">Setup Complete</h2>
            <p style="margin-bottom: 1rem; color: #ccc;">Your system is configured and ready to use.</p>
            <a href="/admin"><button class="success" style="font-size: 1.1em; padding: 0.75rem 2rem;">Continue to Admin Dashboard</button></a>
        </div>
        {{end}}

        <div class="card">
            <h2>System Status</h2>
            <table style="margin-bottom: 1rem;">
                {{range .Requirements}}
                {{if eq .Name "WireGuard Tools"}}
                <tr>
                    <td style="width: 200px; vertical-align: top;">WireGuard</td>
                    <td>
                        {{if not .Installed}}
                        <span class="status-err">● Not installed</span>
                        <code style="font-size: 0.85em; color: #3498db; margin-left: 0.5rem;">{{.Command}}</code>
                        {{else if $.Status.InterfaceUp}}
                        <span class="status-ok">● Running</span>
                        {{else}}
                        <span class="status-err">● Down</span>
                        <form method="POST" action="/admin/interface/up" style="display:inline; margin-left: 0.5rem;">
                            <button class="success" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Bring Up</button>
                        </form>
                        {{end}}
                        <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">VPN tunnel for secure remote access to your homelab network</div>
                    </td>
                </tr>
                {{else if eq .Name "HAProxy"}}
                <tr>
                    <td style="vertical-align: top;">HAProxy</td>
                    <td>
                        {{if .Installed}}
                        <span class="status-ok">● Installed</span>
                        {{else}}
                        <span class="status-err">● Not installed</span>
                        <code style="font-size: 0.85em; color: #3498db; margin-left: 0.5rem;">{{.Command}}</code>
                        {{end}}
                        <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">Reverse proxy that routes incoming HTTPS traffic to your services and handles SSL certificates</div>
                    </td>
                </tr>
                {{else if eq .Name "dnsmasq"}}
                {{if $.Config.DNSMasqEnabled}}
                <tr>
                    <td style="vertical-align: top;">dnsmasq</td>
                    <td>
                        {{if not .Installed}}
                        <span class="status-err">● Not installed</span>
                        <code style="font-size: 0.85em; color: #3498db; margin-left: 0.5rem;">{{.Command}}</code>
                        {{else if $.DNSStatus.Running}}
                        <span class="status-ok">● Running</span>
                        {{else}}
                        <span class="status-err">● Stopped</span>
                        <form method="POST" action="/admin/dnsmasq/start" style="display:inline; margin-left: 0.5rem;">
                            <button class="success" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Start</button>
                        </form>
                        {{end}}
                        <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">DNS server that resolves service names to internal IPs for VPN clients</div>
                    </td>
                </tr>
                {{end}}
                {{end}}
                {{end}}
                <tr>
                    <td style="vertical-align: top;">IP Forwarding</td>
                    <td>
                        {{if .Status.IPForwarding}}
                        <span class="status-ok">● Enabled</span>
                        {{else}}
                        <span class="status-err">● Disabled</span>
                        <form method="POST" action="/admin/enable-forwarding" style="display:inline; margin-left: 0.5rem;">
                            <button class="success" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Enable</button>
                        </form>
                        {{end}}
                        <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">Kernel setting that allows VPN traffic to be routed between network interfaces</div>
                    </td>
                </tr>
                <tr>
                    <td style="vertical-align: top;">NAT Masquerade</td>
                    <td>
                        {{if .Status.Masquerading}}
                        <span class="status-ok">● Active</span>
                        {{else}}
                        <span class="status-err">● Missing</span>
                        <form method="POST" action="/admin/add-masquerade" style="display:inline; margin-left: 0.5rem;">
                            <button class="success" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Add Rule</button>
                        </form>
                        {{end}}
                        <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">Firewall rule that translates VPN client IPs to the server's IP when accessing the local network</div>
                    </td>
                </tr>
            </table>
            <form method="POST" action="/admin/reload" style="display:inline">
                <button type="submit" class="secondary">Reload WireGuard</button>
            </form>
        </div>

        <div class="card">
            <h2>Systemd Service</h2>
            {{if .ServiceInstalled}}
            <p class="status-ok">Systemd service is installed</p>
            {{if .ServiceEnabled}}
            <p class="status-ok">Service is enabled (starts on boot)</p>
            {{else}}
            <p class="status-err">Service is not enabled</p>
            <form method="POST" action="/admin/setup/enable-service" style="margin-bottom: 1rem;">
                <button class="success" type="submit">Enable Service</button>
            </form>
            {{end}}
            {{if .ServiceNeedsUpdate}}
            <p class="status-err">Service file content differs from expected</p>
            <details style="margin: 1rem 0;">
                <summary style="cursor: pointer; font-weight: bold;">View Current Service Content</summary>
                <pre style="margin-top: 0.5rem; font-size: 0.85em; overflow-x: auto;">{{.CurrentServiceContent}}</pre>
            </details>
            <details style="margin: 1rem 0;">
                <summary style="cursor: pointer; font-weight: bold;">View Expected Service Content</summary>
                <pre style="margin-top: 0.5rem; font-size: 0.85em; overflow-x: auto;">{{.ExpectedServiceContent}}</pre>
            </details>
            <form method="POST" action="/admin/setup/install-service">
                <button class="success" type="submit">Update Service File</button>
            </form>
            {{end}}
            {{else}}
            <p class="status-err">Systemd service is not installed</p>
            <p>Install the service to run homelab-horizon as a background service with root privileges.</p>
            <details style="margin: 1rem 0;">
                <summary style="cursor: pointer; font-weight: bold;">Preview Service Content</summary>
                <pre style="margin-top: 0.5rem; font-size: 0.85em; overflow-x: auto;">{{.ExpectedServiceContent}}</pre>
            </details>
            <form method="POST" action="/admin/setup/install-service">
                <button class="success" type="submit">Install Systemd Service</button>
            </form>
            {{end}}
        </div>

        <div class="card">
            <h2>WireGuard Server Config</h2>
            {{if .WGConfigExists}}
            <p class="status-ok">WireGuard config exists at {{.Config.WGConfigPath}}</p>
            {{else}}
            <p class="status-err">WireGuard config not found at {{.Config.WGConfigPath}}</p>
            <p>Create a basic WireGuard server configuration:</p>
            <form method="POST" action="/admin/setup/create-wg-config">
                <button class="success" type="submit">Create Server Config</button>
            </form>
            {{end}}
        </div>

        <div class="card">
            <h2>Configuration</h2>
            <form method="POST" action="/admin/config">
                <details open>
                    <summary style="cursor: pointer; margin-bottom: 1rem; font-weight: bold;">Server Settings</summary>
                    <label>Listen Address</label>
                    <input type="text" name="listen_addr" value="{{.Config.ListenAddr}}" placeholder=":8080">
                    <label>Kiosk URL (for invite QR codes)</label>
                    <input type="text" name="kiosk_url" value="{{.Config.KioskURL}}" placeholder="https://kiosk.vpn.example.com">
                </details>

                <details open>
                    <summary style="cursor: pointer; margin-bottom: 1rem; font-weight: bold;">WireGuard Settings</summary>
                    <label>Interface Name</label>
                    <input type="text" name="wg_interface" value="{{.Config.WGInterface}}" placeholder="wg0">
                    <label>Config Path</label>
                    <input type="text" name="wg_config_path" value="{{.Config.WGConfigPath}}" placeholder="/etc/wireguard/wg0.conf">
                    <label>Server Endpoint (public address)</label>
                    <input type="text" name="server_endpoint" value="{{.Config.ServerEndpoint}}" placeholder="vpn.example.com:51820">
                    <label>Server Public Key</label>
                    <input type="text" name="server_public_key" value="{{.Config.ServerPublicKey}}" placeholder="(auto-detected)">
                </details>

                <details open>
                    <summary style="cursor: pointer; margin-bottom: 1rem; font-weight: bold;">VPN Settings</summary>
                    <label>VPN Range (CIDR)</label>
                    <input type="text" name="vpn_range" value="{{.Config.VPNRange}}" placeholder="10.100.0.0/24">
                    <label>DNS Server (for clients)</label>
                    <input type="text" name="dns" value="{{.Config.DNS}}" placeholder="10.100.0.1">
                    <label>Allowed IPs (client routes, blank = auto-detect VPN + local network)</label>
                    <input type="text" name="allowed_ips" value="{{.Config.AllowedIPs}}" placeholder="{{.Config.GetAllowedIPs}}">
                </details>

                <details>
                    <summary style="cursor: pointer; margin-bottom: 1rem; font-weight: bold;">Files</summary>
                    <label>Invites File</label>
                    <input type="text" name="invites_file" value="{{.Config.InvitesFile}}" placeholder="invites.txt">
                </details>

                <details>
                    <summary style="cursor: pointer; margin-bottom: 1rem; font-weight: bold;">DNSMasq Settings</summary>
                    <label style="display: flex; align-items: center; gap: 0.5rem;">
                        <input type="checkbox" name="dnsmasq_enabled" {{if .Config.DNSMasqEnabled}}checked{{end}} style="width: auto;">
                        Enable DNSMasq Management
                    </label>
                    <label>DNSMasq Config Path</label>
                    <input type="text" name="dnsmasq_config_path" value="{{.Config.DNSMasqConfigPath}}" placeholder="/etc/dnsmasq.d/wg-vpn.conf">
                    <label>DNSMasq Hosts Path</label>
                    <input type="text" name="dnsmasq_hosts_path" value="{{.Config.DNSMasqHostsPath}}" placeholder="/etc/dnsmasq.d/wg-hosts">
                    <label>Additional Interfaces (comma-separated, e.g. eth0)</label>
                    <input type="text" name="dnsmasq_interfaces" value="{{range $i, $s := .Config.DNSMasqInterfaces}}{{if $i}}, {{end}}{{$s}}{{end}}" placeholder="eth0, br0">
                    <label>Upstream DNS Servers (comma-separated)</label>
                    <input type="text" name="upstream_dns" value="{{range $i, $s := .Config.UpstreamDNS}}{{if $i}}, {{end}}{{$s}}{{end}}" placeholder="1.1.1.1, 8.8.8.8">
                </details>

                <div style="margin-top: 1rem;">
                    <button type="submit" class="secondary">Save Configuration</button>
                </div>
            </form>
        </div>

        <div class="card">
            <h2>Manual Installation</h2>
            <pre># Copy binary and run
sudo cp homelab-horizon /usr/local/bin/
sudo mkdir -p /etc/homelab-horizon
cd /etc/homelab-horizon
sudo homelab-horizon

# Then use the Install Service button above to set up systemd</pre>
        </div>
    </div>
    <script>
    // Auto-inject CSRF token into all POST forms
    (function() {
        var meta = document.querySelector('meta[name="csrf-token"]');
        var csrfToken = meta ? meta.getAttribute('content') : '';
        if (!csrfToken) {
            console.error('[CSRF] No CSRF token in meta tag!');
        }
        document.querySelectorAll('form[method="POST"]').forEach(function(form) {
            if (!form.querySelector('input[name="csrf_token"]')) {
                var input = document.createElement('input');
                input.type = 'hidden';
                input.name = 'csrf_token';
                input.value = csrfToken;
                form.appendChild(input);
            }
        });
    })();
    </script>
</body>
</html>`
