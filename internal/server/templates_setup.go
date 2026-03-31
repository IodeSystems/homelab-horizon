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
            <div>
                <h1 style="margin-bottom: 0;">Setup & Configuration</h1>
                <span style="color: #555; font-size: 0.85em;">{{.Version}}</span>
            </div>
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
                        {{if .Error}}<div style="color: #e74c3c; font-size: 0.8em; margin-top: 0.15rem;">{{.Error}}</div>{{end}}
                        <form method="POST" action="/admin/setup/install-requirement" style="display:inline; margin-left: 0.5rem;">
                            <input type="hidden" name="name" value="WireGuard Tools">
                            <button class="success" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Install</button>
                        </form>
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
                        {{if .Error}}<div style="color: #e74c3c; font-size: 0.8em; margin-top: 0.15rem;">{{.Error}}</div>{{end}}
                        <form method="POST" action="/admin/setup/install-requirement" style="display:inline; margin-left: 0.5rem;">
                            <input type="hidden" name="name" value="HAProxy">
                            <button class="success" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Install</button>
                        </form>
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
                        {{if .Error}}<div style="color: #e74c3c; font-size: 0.8em; margin-top: 0.15rem;">{{.Error}}</div>{{end}}
                        <form method="POST" action="/admin/setup/install-requirement" style="display:inline; margin-left: 0.5rem;">
                            <input type="hidden" name="name" value="dnsmasq">
                            <button class="success" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Install</button>
                        </form>
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
                <tr>
                    <td style="vertical-align: top;">IPv6</td>
                    <td>
                        {{if .IPv6Status.Connectivity}}
                        <span class="status-ok">● Working</span>
                        {{if .IPv6Status.PublicIPv6}}<span style="color: #888; margin-left: 0.5rem; font-size: 0.85em;">({{.IPv6Status.PublicIPv6}})</span>{{end}}
                        {{else if .IPv6Status.HasGlobalIP}}
                        <span class="status-err">● Blocked</span>
                        <div style="color: #e74c3c; font-size: 0.85em; margin-top: 0.25rem;">{{.IPv6Status.Recommendation}}</div>
                        {{else if .IPv6Status.HasAddress}}
                        <span style="color: #f39c12;">● Link-local only</span>
                        <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">{{.IPv6Status.Recommendation}}</div>
                        {{else}}
                        <span style="color: #888;">● Not available</span>
                        <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">{{.IPv6Status.Recommendation}}</div>
                        {{end}}
                        <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">IPv6 connectivity status - helps diagnose slow DNS or connection issues</div>
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
                {{if .ServiceEnabled}}
                <p class="status-ok">● Installed and enabled (starts on boot)</p>
                {{else}}
                <p class="status-ok">● Installed</p>
                <p class="status-err">● Not enabled (won't start on boot)</p>
                <form method="POST" action="/admin/setup/enable-service" style="margin-bottom: 1rem;">
                    <button class="success" type="submit">Enable Service</button>
                </form>
                {{end}}
                {{if .ServiceVerifyOK}}
                <p class="status-ok">● Passes systemd-analyze verify</p>
                {{else if .ServiceVerifyOutput}}
                <div style="background: #2d1a1a; border: 1px solid #e74c3c; border-radius: 4px; padding: 1rem; margin: 0.5rem 0;">
                    <p class="status-err" style="margin: 0 0 0.5rem 0;">● systemd-analyze verify found issues</p>
                    <pre style="font-size: 0.8em; color: #e74c3c; margin: 0; white-space: pre-wrap;">{{.ServiceVerifyOutput}}</pre>
                </div>
                {{end}}
                {{if .ServiceNeedsUpdate}}
                <div style="background: #2d1a1a; border: 1px solid #e74c3c; border-radius: 4px; padding: 1rem; margin: 1rem 0;">
                    <p class="status-err" style="margin: 0 0 0.5rem 0;">● Service file differs from expected</p>
                    <p style="color: #ccc; margin: 0; font-size: 0.9em;">The installed service file doesn't match what would be generated. This can happen after moving the binary or config file.</p>
                </div>
                <details open style="margin: 1rem 0;">
                    <summary style="cursor: pointer; font-weight: bold;">Current vs Expected</summary>
                    <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 0.5rem;">
                        <div>
                            <div style="font-size: 0.85em; color: #e74c3c; margin-bottom: 0.25rem;">Current (installed)</div>
                            <pre style="font-size: 0.8em; overflow-x: auto; background: #1a1a2e; padding: 0.5rem; border-radius: 4px; margin: 0;">{{.CurrentServiceContent}}</pre>
                        </div>
                        <div>
                            <div style="font-size: 0.85em; color: #2ecc71; margin-bottom: 0.25rem;">Expected (would generate)</div>
                            <pre style="font-size: 0.8em; overflow-x: auto; background: #1a1a2e; padding: 0.5rem; border-radius: 4px; margin: 0;">{{.ExpectedServiceContent}}</pre>
                        </div>
                    </div>
                </details>
                <form method="POST" action="/admin/setup/install-service">
                    <button class="success" type="submit">Update Service File</button>
                </form>
                {{else}}
                <details style="margin: 1rem 0;">
                    <summary style="cursor: pointer; font-weight: bold;">View Service File</summary>
                    <pre style="margin-top: 0.5rem; font-size: 0.85em; overflow-x: auto; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">{{.CurrentServiceContent}}</pre>
                </details>
                {{end}}
            {{else}}
            <p class="status-err">● Not installed</p>
            <p style="color: #ccc;">Install the service to run homelab-horizon as a background service with root privileges.</p>
            <details style="margin: 1rem 0;">
                <summary style="cursor: pointer; font-weight: bold;">Preview Service File</summary>
                <pre style="margin-top: 0.5rem; font-size: 0.85em; overflow-x: auto; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">{{.ExpectedServiceContent}}</pre>
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
            <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
                <h2 style="margin: 0;">Router & Connectivity</h2>
                <form method="POST" action="/admin/setup/test-connectivity" style="margin: 0;">
                    <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
                    <button type="submit" class="success">Run Tests</button>
                </form>
            </div>
            <p style="color: #ccc; margin-bottom: 1rem;">Verify your router's port forwarding and DNS settings from within the network.</p>
            
            {{if .ConnectivityResults}}
            <table style="margin-bottom: 1rem;">
                <thead>
                    <tr>
                        <th style="text-align: left;">Test</th>
                        <th style="text-align: left;">Status</th>
                        <th style="text-align: left;">Details</th>
                    </tr>
                </thead>
                <tbody>
                    {{range .ConnectivityResults}}
                    <tr>
                        <td>{{.Name}}</td>
                        <td>
                            {{if eq .Status "ok"}}
                            <span class="status-ok">● Passed</span>
                            {{else}}
                            <span class="status-err">● Failed</span>
                            {{end}}
                        </td>
                        <td style="font-size: 0.9em; color: #ccc;">{{.Message}}</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
            {{else}}
            <p style="text-align: center; color: #888; padding: 1rem; border: 1px dashed #444; border-radius: 4px;">No tests run yet. Click "Run Tests" to verify connectivity.</p>
            {{end}}
            
            <div style="margin-top: 1rem; border-top: 1px solid #333; padding-top: 1rem;">
                <h3 style="font-size: 1rem; margin-bottom: 0.5rem;">Manual Setup Steps</h3>
                <ol style="color: #ccc; font-size: 0.9em; padding-left: 1.25rem;">
                    <li><strong>Port Forwarding:</strong> Forward 51820/UDP, 80/TCP, and 443/TCP to this device ({{.Config.ListenAddr}}).</li>
                    <li><strong>Upstream DNS:</strong> Point your router's upstream DNS to this device's local IP.</li>
                </ol>
            </div>
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
                    <label>VPN Gateway Route (networks clients can access via VPN)</label>
                    <div style="display: flex; gap: 1rem; margin: 0.5rem 0;">
                        <label style="display: flex; align-items: center; gap: 0.25rem; cursor: pointer;">
                            <input type="radio" name="gateway_mode" value="local" onchange="setGatewayMode('local')" style="width: auto;">
                            Local Networks
                        </label>
                        <label style="display: flex; align-items: center; gap: 0.25rem; cursor: pointer;">
                            <input type="radio" name="gateway_mode" value="full" onchange="setGatewayMode('full')" style="width: auto;">
                            Full Tunnel
                        </label>
                        <label style="display: flex; align-items: center; gap: 0.25rem; cursor: pointer;">
                            <input type="radio" name="gateway_mode" value="custom" onchange="setGatewayMode('custom')" style="width: auto;">
                            Custom
                        </label>
                    </div>
                    <input type="text" name="allowed_ips" id="allowed_ips" value="{{.Config.AllowedIPs}}" placeholder="{{.Config.DeriveAllowedIPs}}">
                    <div style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">
                        <strong>Local Networks:</strong> VPN + LAN only ({{.Config.DeriveAllowedIPs}}). <strong>Full Tunnel:</strong> All traffic via VPN (0.0.0.0/0).
                    </div>
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
            <h2>Backup & Restore</h2>
            <p style="color: #ccc; margin-bottom: 1rem;">Export a full backup (config, WireGuard keys, SSL certs, invites) or restore from a previous backup. After restoring, run a sync to regenerate derived configs.</p>
            <div class="flex">
                <a href="/admin/backup/export" id="backup-export-link" style="text-decoration: none;"><button type="button" class="secondary">Download Backup</button></a>
                <button type="button" class="secondary" onclick="document.getElementById('restore-modal').classList.add('active')">Restore from Backup</button>
            </div>
        </div>

        <div class="modal-overlay" id="restore-modal">
            <div class="modal-content" style="max-width: 500px;">
                <button class="modal-close" onclick="this.closest('.modal-overlay').classList.remove('active')">&times;</button>
                <h2>Restore from Backup</h2>
                <p style="color: #ccc; margin-bottom: 1rem;">Upload a backup zip file. This will overwrite the current configuration, WireGuard keys, SSL certificates, and admin token.</p>
                <div id="restore-status" style="display: none; margin-bottom: 1rem;"></div>
                <input type="file" id="restore-file" accept=".zip" style="margin-bottom: 1rem;">
                <div class="flex">
                    <button type="button" class="danger" id="restore-btn" onclick="doRestore()">Restore</button>
                    <button type="button" class="secondary" onclick="this.closest('.modal-overlay').classList.remove('active')">Cancel</button>
                </div>
            </div>
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

    // VPN gateway route mode helpers
    var localNetworks = '{{.Config.DeriveAllowedIPs}}';
    var fullTunnel = '0.0.0.0/0, ::/0';

    function setGatewayMode(mode) {
        var input = document.getElementById('allowed_ips');
        if (mode === 'local') {
            input.value = '';
            input.disabled = true;
            input.style.opacity = '0.5';
        } else if (mode === 'full') {
            input.value = fullTunnel;
            input.disabled = true;
            input.style.opacity = '0.5';
        } else {
            input.disabled = false;
            input.style.opacity = '1';
        }
    }

    // Initialize radio based on current value
    (function() {
        var input = document.getElementById('allowed_ips');
        var value = input.value.trim();
        var radios = document.querySelectorAll('input[name="gateway_mode"]');
        if (value === '' || value === localNetworks) {
            radios[0].checked = true; // local
            setGatewayMode('local');
        } else if (value.indexOf('0.0.0.0/0') !== -1) {
            radios[1].checked = true; // full
            setGatewayMode('full');
        } else {
            radios[2].checked = true; // custom
            setGatewayMode('custom');
        }
    })();
    function doRestore() {
        var fileInput = document.getElementById('restore-file');
        var status = document.getElementById('restore-status');
        var btn = document.getElementById('restore-btn');

        if (!fileInput.files.length) {
            status.style.display = 'block';
            status.className = 'error';
            status.textContent = 'Please select a backup zip file.';
            return;
        }

        btn.disabled = true;
        btn.innerHTML = '<span class="loading"></span> Restoring...';
        status.style.display = 'block';
        status.className = 'info';
        status.textContent = 'Uploading and restoring backup...';

        var file = fileInput.files[0];
        fetch('/admin/backup/import', {
            method: 'POST',
            body: file,
        }).then(function(resp) {
            return resp.json().then(function(data) {
                if (resp.ok && data.status === 'ok') {
                    status.className = 'success';
                    status.textContent = 'Restore complete. Run a sync to regenerate derived configs.';
                } else {
                    status.className = 'error';
                    status.textContent = 'Restore had errors: ' + (data.errors || []).join(', ');
                }
            });
        }).catch(function(err) {
            status.className = 'error';
            status.textContent = 'Restore failed: ' + err.message;
        }).finally(function() {
            btn.disabled = false;
            btn.innerHTML = 'Restore';
        });
    }
    </script>
</body>
</html>`
