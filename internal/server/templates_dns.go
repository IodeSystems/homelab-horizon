package server

const dnsTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>External DNS - Homelab Horizon</title>
    <style>` + baseCSS + `</style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>External DNS</h1>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        {{if .Message}}<div class="success">{{.Message}}</div>{{end}}
        {{if .Error}}<div class="error">{{.Error}}</div>{{end}}

        <div class="card">
            <h2>Public IP (NAT)</h2>
            {{if .PublicIP}}
            <p><code style="font-size: 1.2rem;">{{.PublicIP}}</code></p>
            <p style="color: #888; margin-top: 0.5rem;">
                Services with "auto" target IP will use this address for DNS records.
                {{if .Config.PublicIPInterval}}
                <br>Auto-syncs every {{.Config.PublicIPInterval}} seconds when IP changes.
                {{end}}
            </p>
            {{else}}
            <p class="status-err">Could not determine public IP</p>
            {{end}}
        </div>

        <div class="card">
            <h2>Configured Zones ({{len .Zones}})</h2>
            <p style="color: #888; margin-bottom: 1rem;">DNS zones are configured on the admin page. Each zone has a DNS provider for external record management.</p>
            {{if .Zones}}
            <table>
                <thead><tr><th>Zone</th><th>Provider</th><th>User</th><th>Zone ID</th></tr></thead>
                <tbody>
                {{range .Zones}}
                <tr>
                    <td data-label="Zone"><strong>{{.Name}}</strong></td>
                    <td data-label="Provider">
                        {{if .DNSProvider}}
                            {{if eq .DNSProvider.Type "route53"}}<span style="color: #f90;">Route53</span>{{else if eq .DNSProvider.Type "namecom"}}<span style="color: #3498db;">Name.com</span>{{else}}{{.DNSProvider.Type}}{{end}}
                        {{else if .AWSProfile}}
                            <span style="color: #f90;">Route53</span>
                        {{else}}
                            <span style="color: #f90;">Route53</span>
                        {{end}}
                    </td>
                    <td data-label="User">
                        {{if .DNSProvider}}
                            {{if eq .DNSProvider.Type "route53"}}{{if .DNSProvider.AWSProfile}}{{.DNSProvider.AWSProfile}}{{else}}default{{end}}{{else if eq .DNSProvider.Type "namecom"}}{{.DNSProvider.NamecomUsername}}{{end}}
                        {{else if .AWSProfile}}{{.AWSProfile}}{{else}}default{{end}}
                    </td>
                    <td data-label="Zone ID"><code style="font-size: 0.85em;">{{.ZoneID}}</code></td>
                </tr>
                {{end}}
                </tbody>
            </table>
            {{else}}
            <p>No zones configured. <a href="/admin">Add zones</a> to manage DNS records.</p>
            {{end}}
        </div>

        <div class="card">
            <h2>External Services ({{len .Services}})</h2>
            <p style="color: #888; margin-bottom: 1rem;">DNS records are auto-generated from services with ExternalDNS configured. Manage services on the <a href="/admin">admin page</a>.</p>
            {{if .Services}}
            <table>
                <thead><tr><th>Service</th><th>Domain</th><th>Proxy</th><th>Target IP</th><th></th></tr></thead>
                <tbody>
                {{range .Services}}
                <tr>
                    <td data-label="Service"><strong>{{.Name}}</strong></td>
                    <td data-label="Domain"><code>{{.Domain}}</code></td>
                    <td data-label="Proxy">
                        {{if .Proxy}}
                            <span style="color: #3498db;">HAProxy</span>
                        {{else}}
                            <span style="color: #9b59b6;">Direct</span>
                        {{end}}
                    </td>
                    <td data-label="Target IP">
                        {{if .ExternalDNS}}
                            {{if .ExternalDNS.IP}}
                                <code>{{.ExternalDNS.IP}}</code>
                            {{else}}
                                <code title="Uses auto-detected public IP">→ {{$.PublicIP}}</code>
                                <span style="color: #888; font-size: 0.8em;">(auto)</span>
                            {{end}}
                        {{end}}
                    </td>
                    <td>
                        <form method="POST" action="/admin/dns/sync" style="display:inline">
                            <input type="hidden" name="name" value="{{.Domain}}">
                            <button class="success" type="submit">Sync</button>
                        </form>
                    </td>
                </tr>
                {{end}}
                </tbody>
            </table>
            <div style="margin-top: 1rem;">
                <form method="POST" action="/admin/dns/sync-all" style="display:inline">
                    <button class="success" type="submit">Sync All Records</button>
                </form>
            </div>
            {{else}}
            <p>No services with external DNS configured. <a href="/admin">Add a service</a> with ExternalDNS to create DNS records.</p>
            {{end}}
        </div>

        <div class="card">
            <h2>Derived DNS Records ({{len .Records}})</h2>
            <p style="color: #888; margin-bottom: 1rem;">These A records are auto-generated from external services.</p>
            {{if .Records}}
            <table>
                <thead><tr><th>Domain</th><th>Type</th><th>Target</th><th>TTL</th><th>Zone</th></tr></thead>
                <tbody>
                {{range .Records}}
                <tr>
                    <td data-label="Domain"><code>{{.Name}}</code></td>
                    <td data-label="Type">{{.Type}}</td>
                    <td data-label="Target"><code>{{if .Value}}{{.Value}}{{else}}(auto){{end}}</code></td>
                    <td data-label="TTL">{{.TTL}}s</td>
                    <td data-label="Zone">{{.ZoneName}}</td>
                </tr>
                {{end}}
                </tbody>
            </table>
            {{else}}
            <p>No records to sync.</p>
            {{end}}
        </div>

        <div class="card">
            <h2>How Split-Horizon DNS Works</h2>
            <ul style="padding-left: 1.5rem; line-height: 1.8;">
                <li><strong>Internal (VPN):</strong> dnsmasq resolves domains to internal backend IPs</li>
                <li><strong>External (Internet):</strong> DNS provider resolves domains to public IPs</li>
                <li><strong>HAProxy Mode:</strong> Public IP is your VPN server's IP, traffic routed via HAProxy</li>
                <li><strong>Direct Mode:</strong> Public IP is the service's own IP (for services with public IPs)</li>
            </ul>
            <p style="margin-top: 1rem; color: #888;">
                Tip: Enable auto-sync in config to automatically update records when your public IP changes.
            </p>
        </div>
    </div>
    <script>
    // Auto-inject CSRF token into all POST forms
    (function() {
        var csrfToken = document.querySelector('meta[name="csrf-token"]').getAttribute('content');
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
