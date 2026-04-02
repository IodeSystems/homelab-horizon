package server

const domainsTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>Domain Analysis - Homelab Horizon</title>
    <style>` + baseCSS + `
    .dot { font-size: 1.2em; }
    .dot-ok { color: #2ecc71; }
    .dot-warn { color: #f39c12; }
    .dot-err { color: #555; }
    .summary-grid { display: flex; gap: 1.5rem; flex-wrap: wrap; }
    .summary-item { text-align: center; }
    .summary-item .num { font-size: 1.5rem; font-weight: bold; color: #fff; }
    .summary-item .label { font-size: 0.8rem; color: #888; }
    .domain-row { cursor: pointer; }
    .domain-row:hover { background: #1a2a4a; }
    .detail-row td { padding: 0 !important; border-bottom: none; }
    .detail-inner { padding: 1rem; background: #0f3460; border-radius: 0 0 6px 6px; }
    .detail-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 0.75rem; }
    .detail-card { background: #16213e; border-radius: 6px; padding: 0.75rem; }
    .detail-card h3 { font-size: 0.85rem; color: #888; margin-bottom: 0.5rem; }
    .detail-card .val { font-size: 0.9rem; }
    .actions { margin-top: 0.75rem; padding-top: 0.75rem; border-top: 1px solid #1a2a4a; }
    .san-list { list-style: none; padding: 0; margin: 0.25rem 0 0 0; }
    .san-list li { font-size: 0.85rem; padding: 0.1rem 0; }
    .san-list li code { font-size: 0.8rem; }
    .match { color: #2ecc71; }
    .mismatch { color: #e74c3c; }
    .noresolve { color: #555; }
    @media screen and (max-width: 640px) {
        .detail-grid { grid-template-columns: 1fr; }
        .summary-grid { gap: 1rem; }
    }
    </style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>Domain Analysis</h1>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        {{if .Message}}<div class="success">{{.Message}}</div>{{end}}
        {{if .Error}}<div class="error">{{.Error}}</div>{{end}}

        <div class="card">
            <h2>Summary</h2>
            <div class="summary-grid">
                <div class="summary-item">
                    <div class="num">{{.TotalCount}}</div>
                    <div class="label">Domains</div>
                </div>
                <div class="summary-item">
                    <div class="num">{{.IntDNSCount}}</div>
                    <div class="label">Internal DNS</div>
                </div>
                <div class="summary-item">
                    <div class="num">{{.ExtDNSCount}}</div>
                    <div class="label">External DNS</div>
                </div>
                <div class="summary-item">
                    <div class="num">{{.HTTPSCount}}</div>
                    <div class="label">HTTPS</div>
                </div>
                <div class="summary-item">
                    <div class="num">{{.ProxyCount}}</div>
                    <div class="label">Proxy</div>
                </div>
            </div>
        </div>

        {{if .ZoneSSLStatuses}}
        <div class="card">
            <details>
                <summary style="cursor:pointer; font-weight:bold;">Zone SSL Status ({{len .ZoneSSLStatuses}} zones)</summary>
                <div style="margin-top: 0.75rem;">
                {{range .ZoneSSLStatuses}}
                <div style="background:#0f3460; border-radius:6px; padding:0.75rem; margin-bottom:0.5rem;">
                    <div style="display:flex; justify-content:space-between; align-items:center;">
                        <strong>{{.ZoneName}}</strong>
                        {{if not .SSLEnabled}}<span style="color:#555">SSL disabled</span>
                        {{else if .CertExists}}<span class="status-ok">cert valid</span>
                        {{else}}<span class="status-err">no cert</span>{{end}}
                    </div>
                    {{if .SSLEnabled}}
                    <div style="display:grid; grid-template-columns:1fr 1fr; gap:0.75rem; margin-top:0.5rem;">
                        <div>
                            <div style="color:#888; font-size:0.8rem; margin-bottom:0.25rem;">Configured SubZones</div>
                            {{if .ConfiguredDomains}}
                            <ul class="san-list">
                                {{range .ConfiguredDomains}}<li><code>{{.}}</code></li>{{end}}
                            </ul>
                            {{else}}<span style="color:#555; font-size:0.85rem;">None configured</span>{{end}}
                        </div>
                        <div>
                            <div style="color:#888; font-size:0.8rem; margin-bottom:0.25rem;">Actual Certificate SANs</div>
                            {{if .CertExists}}
                            <ul class="san-list">
                                {{range .ActualSANs}}<li><code>{{.}}</code></li>{{end}}
                            </ul>
                            <div style="color:#888; font-size:0.75rem; margin-top:0.25rem;">
                                Expires: {{.CertExpiry}}
                                {{if .CertIssuer}}<br>Issuer: {{.CertIssuer}}{{end}}
                            </div>
                            {{else}}<span style="color:#555; font-size:0.85rem;">No certificate on disk</span>{{end}}
                        </div>
                    </div>
                    {{if .MissingSANs}}
                    <div style="margin-top:0.5rem; color:#f39c12; font-size:0.85rem;">
                        Missing from cert: {{range .MissingSANs}}<code>{{.}}</code> {{end}}
                        <span style="color:#888">&mdash; run Sync to update certificate</span>
                    </div>
                    {{end}}
                    {{if .ExtraSANs}}
                    <div style="margin-top:0.25rem; color:#888; font-size:0.85rem;">
                        Extra on cert (no longer configured): {{range .ExtraSANs}}<code>{{.}}</code> {{end}}
                    </div>
                    {{end}}
                    {{end}}
                </div>
                {{end}}
                </div>
            </details>
        </div>
        {{end}}

        {{if .SSLGaps}}
        <div class="card" style="border-left: 3px solid #f39c12;">
            <h2 style="color: #f39c12;">SSL Coverage Gaps ({{len .SSLGaps}})</h2>
            <p style="color: #888; margin-bottom: 0.75rem;">
                These service domains are not covered by any SSL certificate SubZone.
                Wildcard certs (<code>*.example.com</code>) only match <strong>one level</strong> of subdomain &mdash;
                <code>app.example.com</code> is covered, but <code>app.vpn.example.com</code> is <strong>not</strong>.
                Multi-level subdomains need their own wildcard SubZone (e.g., <code>*.vpn</code>).
            </p>
            <table>
                <thead><tr><th>Domain</th><th>Needs SubZone</th><th>Why</th><th></th></tr></thead>
                <tbody>
                {{range .SSLGaps}}
                <tr>
                    <td data-label="Domain"><code>{{.Domain}}</code></td>
                    <td data-label="SubZone"><code>{{.Display}}</code></td>
                    <td data-label="Why" style="color:#888; font-size:0.85em;">{{.Reason}}</td>
                    <td>
                        <form method="POST" action="/admin/zone/subzone" style="display:inline">
                            <input type="hidden" name="zone" value="{{.ZoneName}}">
                            <input type="hidden" name="subzone" value="{{.SubZone}}">
                            <button class="success" type="submit" style="white-space:nowrap">Add {{.SubZone}}</button>
                        </form>
                    </td>
                </tr>
                {{end}}
                </tbody>
            </table>
        </div>
        {{end}}

        <div class="card">
            <h2>All Domains ({{.TotalCount}})</h2>
            {{if .Domains}}
            <table>
                <thead>
                    <tr>
                        <th>Domain</th>
                        <th>Service</th>
                        <th style="text-align:center">Int DNS</th>
                        <th style="text-align:center">Ext DNS</th>
                        <th style="text-align:center">HTTPS</th>
                        <th style="text-align:center">Proxy</th>
                    </tr>
                </thead>
                <tbody>
                {{range .Domains}}
                <tr class="domain-row" onclick="toggleDetail('{{.DomainID}}')">
                    <td data-label="Domain"><code>{{.Domain}}</code></td>
                    <td data-label="Service">{{if .HasService}}{{.ServiceName}}{{else}}<span style="color:#555">unbound</span>{{end}}</td>
                    <td data-label="Int DNS" style="text-align:center">
                        {{if .HasInternalDNS}}<span class="dot dot-ok" title="{{.InternalIP}}">&#9679;</span>
                        {{else}}<span class="dot dot-err">&#9679;</span>{{end}}
                    </td>
                    <td data-label="Ext DNS" style="text-align:center">
                        {{if .HasExternalDNS}}<span class="dot dot-ok" title="{{.ExternalIP}}">&#9679;</span>
                        {{else}}<span class="dot dot-err">&#9679;</span>{{end}}
                    </td>
                    <td data-label="HTTPS" style="text-align:center">
                        {{if .CertExists}}<span class="dot dot-ok" title="Cert exists">&#9679;</span>
                        {{else if .HasSSLCoverage}}<span class="dot dot-warn" title="Covered but no cert on disk">&#9679;</span>
                        {{else}}<span class="dot dot-err">&#9679;</span>{{end}}
                    </td>
                    <td data-label="Proxy" style="text-align:center">
                        {{if .HasProxy}}<span class="dot dot-ok" title="{{.ProxyBackend}}">&#9679;</span>
                        {{else}}<span class="dot dot-err">&#9679;</span>{{end}}
                    </td>
                </tr>
                <tr class="detail-row" id="detail-{{.DomainID}}" style="display:none;">
                    <td colspan="6">
                        <div class="detail-inner">
                            <div class="detail-grid">
                                <!-- Zone -->
                                <div class="detail-card">
                                    <h3>Zone</h3>
                                    {{if .HasZone}}
                                    <div class="val"><code>{{.ZoneName}}</code></div>
                                    <div style="color:#888; font-size:0.8rem; margin-top:0.25rem;">
                                        SSL: {{if .ZoneHasSSL}}<span class="status-ok">enabled</span>{{else}}<span class="status-err">disabled</span>{{end}}
                                    </div>
                                    {{else}}
                                    <div class="val status-err">No zone configured for this domain</div>
                                    {{end}}
                                </div>

                                <!-- Service -->
                                <div class="detail-card">
                                    <h3>Service</h3>
                                    {{if .HasService}}
                                    <div class="val"><strong>{{.ServiceName}}</strong></div>
                                    {{else}}
                                    <div class="val" style="color:#555">Not bound to any service</div>
                                    {{end}}
                                </div>

                                <!-- Internal DNS -->
                                <div class="detail-card">
                                    <h3>Internal DNS (dnsmasq)</h3>
                                    {{if .HasInternalDNS}}
                                    <div class="val">Configured: <code>{{.InternalIP}}</code></div>
                                    <div style="font-size:0.85rem; margin-top:0.25rem;">
                                        dnsmasq resolve:
                                        {{if .DnsmasqResolvedIP}}
                                            <code>{{.DnsmasqResolvedIP}}</code>
                                            {{if .DnsmasqDNSMatch}}<span class="match">&#10003; match</span>
                                            {{else}}<span class="mismatch">&#10007; mismatch</span>{{end}}
                                        {{else}}<span class="noresolve">no response</span>{{end}}
                                    </div>
                                    {{else}}
                                    <div class="val" style="color:#555">Not configured</div>
                                    {{if .DnsmasqResolvedIP}}
                                    <div style="font-size:0.85rem; margin-top:0.25rem;">
                                        dnsmasq resolve: <code>{{.DnsmasqResolvedIP}}</code>
                                    </div>
                                    {{end}}
                                    {{end}}
                                </div>

                                <!-- External DNS -->
                                <div class="detail-card">
                                    <h3>External DNS</h3>
                                    {{if .HasExternalDNS}}
                                    <div class="val">Configured: <code>{{.ExternalIP}}</code></div>
                                    <div style="font-size:0.85rem; margin-top:0.25rem;">
                                        Remote resolve (1.1.1.1):
                                        {{if .RemoteResolvedIP}}
                                            <code>{{.RemoteResolvedIP}}</code>
                                            {{if .RemoteDNSMatch}}<span class="match">&#10003; match</span>
                                            {{else}}<span class="mismatch">&#10007; mismatch</span>{{end}}
                                        {{else}}<span class="noresolve">no response</span>{{end}}
                                    </div>
                                    {{else}}
                                    <div class="val" style="color:#555">Not configured</div>
                                    {{if .RemoteResolvedIP}}
                                    <div style="font-size:0.85rem; margin-top:0.25rem;">
                                        Remote resolve (1.1.1.1): <code>{{.RemoteResolvedIP}}</code>
                                    </div>
                                    {{end}}
                                    {{end}}
                                </div>

                                <!-- SSL/HTTPS -->
                                <div class="detail-card">
                                    <h3>HTTPS / SSL</h3>
                                    {{if .CertExists}}
                                    <div class="val"><span class="status-ok">&#10003;</span> Certificate exists</div>
                                    <div style="color:#888; font-size:0.8rem; margin-top:0.25rem;">
                                        Covered by: <code>{{.CertDomain}}</code><br>
                                        Expires: {{.CertExpiry}}
                                    </div>
                                    {{else if .HasSSLCoverage}}
                                    <div class="val"><span style="color:#f39c12">&#9888;</span> Covered by <code>{{.CertDomain}}</code> but no cert on disk</div>
                                    {{else if .ZoneHasSSL}}
                                    <div class="val"><span style="color:#f39c12">&#9888;</span> Zone has SSL but domain not covered</div>
                                    {{if .NeededSubZoneDisplay}}
                                    <div style="color:#888; font-size:0.8rem; margin-top:0.25rem;">
                                        Needs SubZone: <code>{{.NeededSubZone}}</code> &rarr; <code>{{.NeededSubZoneDisplay}}</code>
                                    </div>
                                    {{end}}
                                    {{else}}
                                    <div class="val" style="color:#555">No HTTPS configured</div>
                                    {{end}}
                                </div>

                                <!-- HAProxy -->
                                <div class="detail-card">
                                    <h3>Reverse Proxy (HAProxy)</h3>
                                    {{if .HasProxy}}
                                    <div class="val"><span class="status-ok">&#10003;</span> <code>{{.ProxyBackend}}</code></div>
                                    <div style="color:#888; font-size:0.8rem; margin-top:0.25rem;">
                                        {{if .InternalOnly}}Internal only{{else}}Public{{end}}
                                        {{if .HasHealthCheck}} &middot; Health: <code>{{.HealthPath}}</code>{{end}}
                                    </div>
                                    {{else}}
                                    <div class="val" style="color:#555">No proxy configured</div>
                                    {{end}}
                                </div>
                            </div>

                            {{if or .CanSyncDNS .CanEnableHTTPS .CanRequestCert}}
                            <div class="actions">
                                {{if .CanSyncDNS}}
                                <form method="POST" action="/admin/dns/sync" style="display:inline">
                                    <input type="hidden" name="name" value="{{.Domain}}">
                                    <button class="success" type="submit">Sync External DNS</button>
                                </form>
                                {{end}}

                                {{if .CanEnableHTTPS}}
                                <form method="POST" action="/admin/zone/subzone" style="display:inline">
                                    <input type="hidden" name="zone" value="{{.ZoneName}}">
                                    <input type="hidden" name="subzone" value="{{.NeededSubZone}}">
                                    <button class="success" type="submit" title="Add SubZone {{.NeededSubZone}} to zone {{.ZoneName}}">Enable HTTPS (add {{.NeededSubZoneDisplay}})</button>
                                </form>
                                {{end}}

                                {{if .CanRequestCert}}
                                <form method="POST" action="/admin/ssl/request-cert" style="display:inline">
                                    <input type="hidden" name="zone" value="{{.ZoneName}}">
                                    <button class="success" type="submit">Request Certificate</button>
                                </form>
                                {{end}}
                            </div>
                            {{end}}
                        </div>
                    </td>
                </tr>
                {{end}}
                </tbody>
            </table>
            {{else}}
            <p>No domains configured. <a href="/admin">Add a service</a> to get started.</p>
            {{end}}
        </div>
    </div>
    <script>
    function toggleDetail(domainId) {
        var el = document.getElementById('detail-' + domainId);
        if (el) {
            el.style.display = el.style.display === 'none' ? 'table-row' : 'none';
        }
    }
    // Auto-inject CSRF token into all POST forms
    (function() {
        var meta = document.querySelector('meta[name="csrf-token"]');
        var csrfToken = meta ? meta.getAttribute('content') : '';
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
