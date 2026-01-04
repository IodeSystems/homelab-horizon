package server

const testTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>System Status - Homelab Horizon</title>
    <style>` + baseCSS + `</style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>System Status</h1>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        {{if .Message}}<div class="success">{{.Message}}</div>{{end}}
        {{if .Error}}<div class="error">{{.Error}}</div>{{end}}

        <div class="card">
            <h2>WireGuard Interface ({{.Config.WGInterface}})</h2>
            {{if .Status.InterfaceUp}}
            <p class="status-ok">Interface is UP</p>
            {{else}}
            <p class="status-err">Interface is DOWN: {{.Status.InterfaceError}}</p>
            <form method="POST" action="/admin/interface/up">
                <button class="success" type="submit">Bring Interface Up</button>
            </form>
            {{end}}
        </div>

        <div class="card">
            <h2>IP Forwarding</h2>
            {{if .Status.IPForwarding}}
            <p class="status-ok">IP forwarding is enabled</p>
            {{else}}
            <p class="status-err">IP forwarding is disabled: {{.Status.ForwardingError}}</p>
            <form method="POST" action="/admin/enable-forwarding">
                <button class="success" type="submit">Enable IP Forwarding</button>
            </form>
            {{end}}
        </div>

        <div class="card">
            <h2>NAT Masquerading ({{.Config.VPNRange}})</h2>
            {{if .Status.Masquerading}}
            <p class="status-ok">Masquerade rule is active</p>
            {{else}}
            <p class="status-err">Masquerade rule not found: {{.Status.MasqError}}</p>
            <form method="POST" action="/admin/add-masquerade">
                <button class="success" type="submit">Add Masquerade Rule</button>
            </form>
            {{end}}
        </div>

        {{if .Config.DNSMasqEnabled}}
        <div class="card">
            <h2>DNSMasq Status</h2>
            {{if .DNSStatus.Running}}
            <p class="status-ok">dnsmasq is running</p>
            {{else}}
            <p class="status-err">dnsmasq is not running</p>
            <form method="POST" action="/admin/dnsmasq/start">
                <button class="success" type="submit">Start dnsmasq</button>
            </form>
            {{end}}
            {{if .DNSStatus.ConfigExists}}
            <p class="status-ok">Config file exists at {{.Config.DNSMasqConfigPath}}</p>
            {{else}}
            <p class="status-err">Config file not found</p>
            <form method="POST" action="/admin/dnsmasq/init">
                <button class="success" type="submit">Create dnsmasq Config</button>
            </form>
            {{end}}
        </div>
        {{end}}

        <div class="card">
            <h2>Quick Setup Commands</h2>
            <pre># Enable IP forwarding
echo 1 > /proc/sys/net/ipv4/ip_forward
sysctl -w net.ipv4.ip_forward=1

# Make permanent
echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf

# Add masquerade rule
iptables -t nat -A POSTROUTING -s {{.Config.VPNRange}} -j MASQUERADE

# Save iptables rules (Debian/Ubuntu)
iptables-save > /etc/iptables/rules.v4

# Install dnsmasq
apt install dnsmasq
systemctl enable dnsmasq</pre>
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

const inviteTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Get Your VPN Config - Homelab Horizon</title>
    <style>` + baseCSS + `
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

const clientConfigTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>Client Config - Homelab Horizon</title>
    <style>` + baseCSS + `</style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>New Client: {{.Name}}</h1>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        <div class="card">
            <div class="info">
                <strong>Important:</strong> This is the only time you'll see this configuration. Copy or download it now!
            </div>
            <h2>Scan QR Code (Mobile)</h2>
            <div style="background: white; padding: 1rem; border-radius: 8px; display: inline-block; margin-bottom: 1rem;">
                {{if .QRCode}}{{.QRCode}}{{end}}
            </div>
            <h2>Or Download Config File</h2>
            <pre>{{.Config}}</pre>
            <form method="POST" action="/admin/client/download">
                <input type="hidden" name="config" value="{{.Config}}">
                <input type="hidden" name="name" value="{{.Name}}">
                <button type="submit" class="success">Download .conf File</button>
            </form>
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
