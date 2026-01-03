package server

const haproxyTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>HAProxy - Homelab Horizon</title>
    <style>` + baseCSS + `</style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>HAProxy Management</h1>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        {{if .Message}}<div class="success">{{.Message}}</div>{{end}}
        {{if .Error}}<div class="error">{{.Error}}</div>{{end}}

        {{if not .HAProxyAvail}}
        <div class="error">
            HAProxy is not installed. Install with: <code>apt install haproxy</code>
        </div>
        {{end}}

        <div class="card">
            <h2>HAProxy Status</h2>
            {{if .HAProxyStatus.Running}}
            <p class="status-ok">HAProxy is running</p>
            {{if .HAProxyStatus.Version}}<p style="color: #888;">{{.HAProxyStatus.Version}}</p>{{end}}
            {{else}}
            <p class="status-err">HAProxy is not running</p>
            <form method="POST" action="/admin/haproxy/start" style="display:inline">
                <button class="success" type="submit">Start HAProxy</button>
            </form>
            {{end}}
            {{if .HAProxyStatus.ConfigExists}}
            <p class="status-ok">Config file exists</p>
            {{else}}
            <p class="status-err">Config file not found at {{.Config.HAProxyConfigPath}}</p>
            {{end}}
        </div>

        <div class="card">
            <h2>Backends ({{len .Backends}})</h2>
            <p style="color: #888; margin-bottom: 1rem;">HAProxy backends are auto-generated from services with external access (mode: haproxy). Manage services on the <a href="/admin">admin page</a>.</p>
            {{if .Backends}}
            <table>
                <thead><tr><th>Service</th><th>Domain</th><th>Backend</th><th>Health</th>{{if .CheckHealth}}<th>Status</th>{{end}}</tr></thead>
                <tbody>
                {{if .CheckHealth}}
                {{range .BackendStatuses}}
                <tr>
                    <td data-label="Service"><strong>{{.Name}}</strong></td>
                    <td data-label="Domain"><code>{{.DomainMatch}}</code></td>
                    <td data-label="Backend"><code>{{.Server}}</code></td>
                    <td data-label="Health">{{if .HTTPCheck}}{{if .CheckPath}}{{.CheckPath}}{{else}}/{{end}}{{else}}-{{end}}</td>
                    <td data-label="Status">
                        {{if .Healthy}}
                        <span class="status-ok">● Healthy</span>
                        {{else}}
                        <span class="status-err">● Unhealthy</span>
                        {{if .Error}}<br><small>{{.Error}}</small>{{end}}
                        {{end}}
                    </td>
                </tr>
                {{end}}
                {{else}}
                {{range .Backends}}
                <tr>
                    <td data-label="Service"><strong>{{.Name}}</strong></td>
                    <td data-label="Domain"><code>{{.DomainMatch}}</code></td>
                    <td data-label="Backend"><code>{{.Server}}</code></td>
                    <td data-label="Health">{{if .HTTPCheck}}{{if .CheckPath}}{{.CheckPath}}{{else}}/{{end}}{{else}}-{{end}}</td>
                </tr>
                {{end}}
                {{end}}
                </tbody>
            </table>
            <div style="margin-top: 0.5rem;">
                <a href="/admin/haproxy?check=1"><button type="button" class="secondary">Check Health</button></a>
            </div>
            {{else}}
            <p>No backends configured. <a href="/admin">Add a service</a> with external access (mode: haproxy) to create backends.</p>
            {{end}}
        </div>

        <div class="card">
            <h2>Actions</h2>
            <div class="flex">
                <form method="POST" action="/admin/haproxy/write-config" style="display:inline">
                    <button type="submit" class="secondary">Write Config</button>
                </form>
                <form method="POST" action="/admin/haproxy/reload" style="display:inline">
                    <button type="submit" class="secondary">Reload HAProxy</button>
                </form>
            </div>
            <p style="color: #888; margin-top: 0.5rem; font-size: 0.9rem;">
                Write Config generates the HAProxy configuration file. Reload applies changes.
            </p>

            <details style="margin-top: 1rem;">
                <summary style="cursor: pointer; color: #e94560;">Preview Config</summary>
                <pre style="margin-top: 0.5rem; max-height: 400px; overflow-y: auto;">{{.ConfigPreview}}</pre>
            </details>
        </div>

        <div class="card">
            <h2>Settings</h2>
            <form method="POST" action="/admin/haproxy/settings">
                <label style="display: flex; align-items: center; gap: 0.5rem;">
                    <input type="checkbox" name="haproxy_enabled" {{if .Config.HAProxyEnabled}}checked{{end}} style="width: auto;">
                    Enable HAProxy Management (auto-start on service startup)
                </label>
                <label>Config Path</label>
                <input type="text" name="haproxy_config_path" value="{{.Config.HAProxyConfigPath}}" placeholder="/etc/haproxy/haproxy.cfg">
                <div class="flex">
                    <div style="flex: 1;">
                        <label>HTTP Port</label>
                        <input type="number" name="haproxy_http_port" value="{{.Config.HAProxyHTTPPort}}" placeholder="80">
                    </div>
                    <div style="flex: 1;">
                        <label>HTTPS Port</label>
                        <input type="number" name="haproxy_https_port" value="{{.Config.HAProxyHTTPSPort}}" placeholder="443">
                    </div>
                </div>
                <button type="submit" class="secondary">Save Settings</button>
            </form>
        </div>

        <div class="card">
            <h2>SSL / Let's Encrypt</h2>

            <p style="color: #888; margin-bottom: 1rem;">SSL certificates are auto-generated as wildcards for zones with SSL enabled. Manage zones on the <a href="/admin">admin page</a>.</p>

            <h3>Wildcard Certificates ({{len .SSLStatus.Domains}})</h3>
            {{if .SSLStatus.Domains}}
            <table>
                <thead><tr><th>Domain</th><th>Email</th><th>Provider</th><th>Status</th><th>Actions</th></tr></thead>
                <tbody>
                {{range .SSLStatus.Domains}}
                <tr>
                    <td data-label="Domain"><code>{{.Domain}}</code></td>
                    <td data-label="Email">{{.Email}}</td>
                    <td data-label="Provider">{{if .ProviderType}}{{.ProviderType}}{{if .AWSProfile}} ({{.AWSProfile}}){{end}}{{else}}-{{end}}</td>
                    <td data-label="Status">
                        {{if .CertExists}}
                        <span class="status-ok">● Valid</span>
                        {{if .ExpiryInfo}}<br><small>Expires: {{.ExpiryInfo}}</small>{{end}}
                        {{if .HAProxyCertReady}}<br><small class="status-ok">HAProxy ready</small>{{end}}
                        {{else}}
                        <span class="status-err">● Not issued</span>
                        {{end}}
                    </td>
                    <td data-label="Actions">
                        {{if .CertExists}}
                        <button type="button" class="secondary" onclick="showCertInfo('{{.Domain}}')">Info</button>
                        {{else}}
                        <form method="POST" action="/admin/ssl/request-cert" style="display:inline">
                            <input type="hidden" name="zone" value="{{.Domain}}">
                            <button class="success" type="submit">Request Cert</button>
                        </form>
                        {{end}}
                    </td>
                </tr>
                {{end}}
                </tbody>
            </table>
            {{else}}
            <p>No SSL-enabled zones configured. <a href="/admin">Add a zone</a> with SSL email to enable wildcard certificates.</p>
            {{end}}

            <h3 style="margin-top: 1.5rem;">SSL Actions</h3>
            <div class="flex">
                <form method="POST" action="/admin/ssl/package-certs" style="display:inline">
                    <button type="submit" class="secondary">Package Certs for HAProxy</button>
                </form>
            </div>
            <p style="font-size: 0.9rem; color: #888; margin-top: 0.5rem;">
                Certificate renewal is handled automatically by the built-in scheduler.
            </p>
        </div>

        <div class="card">
            <h2>SSL Settings</h2>
            <form method="POST" action="/admin/ssl/settings">
                <label style="display: flex; align-items: center; gap: 0.5rem;">
                    <input type="checkbox" name="ssl_enabled" {{if .Config.SSLEnabled}}checked{{end}} style="width: auto;">
                    Enable SSL (use certs in HAProxy config)
                </label>
                <label>Let's Encrypt Cert Directory</label>
                <input type="text" name="ssl_cert_dir" value="{{.Config.SSLCertDir}}" placeholder="/etc/letsencrypt">
                <label>HAProxy Certs Directory</label>
                <input type="text" name="ssl_haproxy_cert_dir" value="{{.Config.SSLHAProxyCertDir}}" placeholder="/etc/haproxy/certs">
                <button type="submit" class="secondary">Save SSL Settings</button>
            </form>
        </div>

        <div class="card">
            <h2>How Domain Matching Works</h2>
            <p>HAProxy routes requests based on the <code>Host</code> header suffix:</p>
            <ul style="padding-left: 1.5rem; line-height: 1.8;">
                <li><code>.example.com</code> matches <code>app1.example.com</code>, <code>api.example.com</code>, etc.</li>
                <li><code>.vpn.local</code> matches <code>app.vpn.local</code>, <code>service.vpn.local</code></li>
                <li>Requests are forwarded to the configured server address</li>
            </ul>
        </div>
    </div>

    <div id="cert-info-modal" class="modal-overlay" onclick="if(event.target===this)closeCertInfoModal()">
        <div class="modal-content" style="max-width: 600px;">
            <button class="modal-close" onclick="closeCertInfoModal()">&times;</button>
            <h2 style="margin-bottom: 1rem;">Certificate Info</h2>
            <div id="cert-info-loading" style="text-align: center; padding: 2rem;">
                <span class="loading"></span> Loading...
            </div>
            <div id="cert-info-content" style="display: none;">
                <table style="width: 100%;">
                    <tr><th style="width: 120px;">Domain</th><td id="cert-info-domain"></td></tr>
                    <tr><th>Issuer</th><td id="cert-info-issuer" style="font-size: 0.9em;"></td></tr>
                    <tr><th>Valid From</th><td id="cert-info-from"></td></tr>
                    <tr><th>Valid Until</th><td id="cert-info-until"></td></tr>
                    <tr><th>Serial</th><td id="cert-info-serial" style="font-family: monospace; font-size: 0.85em;"></td></tr>
                </table>
                <h3 style="margin-top: 1rem; margin-bottom: 0.5rem;">Subject Alternative Names (SANs)</h3>
                <div id="cert-info-sans" style="background: #0a0a1a; padding: 1rem; border-radius: 4px; font-family: monospace; font-size: 0.9em; max-height: 200px; overflow-y: auto;"></div>
            </div>
            <div id="cert-info-error" class="error" style="display: none;"></div>
        </div>
    </div>

    <script>
    function showCertInfo(domain) {
        var modal = document.getElementById('cert-info-modal');
        var loading = document.getElementById('cert-info-loading');
        var content = document.getElementById('cert-info-content');
        var errorDiv = document.getElementById('cert-info-error');

        loading.style.display = 'block';
        content.style.display = 'none';
        errorDiv.style.display = 'none';
        modal.classList.add('active');

        fetch('/admin/ssl/cert-info?domain=' + encodeURIComponent(domain))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                loading.style.display = 'none';

                if (data.error) {
                    errorDiv.textContent = data.error;
                    errorDiv.style.display = 'block';
                    return;
                }

                document.getElementById('cert-info-domain').textContent = data.domain || domain;
                document.getElementById('cert-info-issuer').textContent = data.issuer || '-';
                document.getElementById('cert-info-from').textContent = data.not_before || '-';
                document.getElementById('cert-info-until').textContent = data.not_after || '-';
                document.getElementById('cert-info-serial').textContent = data.serial || '-';

                var sansDiv = document.getElementById('cert-info-sans');
                if (data.sans && data.sans.length > 0) {
                    sansDiv.innerHTML = data.sans.map(function(san) {
                        return '<div style="margin-bottom: 0.25rem;">• ' + san + '</div>';
                    }).join('');
                } else {
                    sansDiv.innerHTML = '<span style="color: #888;">No SANs found</span>';
                }

                content.style.display = 'block';
            })
            .catch(function(err) {
                loading.style.display = 'none';
                errorDiv.textContent = 'Failed to load certificate info: ' + err.message;
                errorDiv.style.display = 'block';
            });
    }

    function closeCertInfoModal() {
        document.getElementById('cert-info-modal').classList.remove('active');
    }

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
