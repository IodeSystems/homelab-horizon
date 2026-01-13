package server

const adminTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="csrf-token" content="{{.CSRFToken}}">
    <title>Admin - Homelab Horizon</title>
    <style>` + baseCSS + `</style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <h1>Homelab Horizon</h1>
            <div>
                <a href="/admin/checks"><button class="secondary">Checks</button></a>
                <a href="/admin/haproxy"><button class="secondary">HAProxy</button></a>
                <a href="/admin/dns"><button class="secondary">External DNS</button></a>
                <a href="/admin/setup"><button class="secondary">Setup</button></a>
                <a href="/admin/help"><button class="secondary">Help</button></a>
                <a href="/logout"><button class="secondary">Logout</button></a>
            </div>
        </div>

        {{if .Message}}<div class="success">{{.Message}}</div>{{end}}
        {{if .Error}}<div class="error">{{.Error}}</div>{{end}}

        <div class="card">
            <h2>Interface: {{.Config.WGInterface}}</h2>
            {{if .InterfaceUp}}
            <p class="status-ok">Interface is UP on port {{.InterfacePort}}</p>
            <div style="display: flex; gap: 2rem; margin-top: 0.5rem; flex-wrap: wrap;">
                <div><span style="color: #888;">NAT IP:</span> <code>{{.NATIP}}</code></div>
                <div><span style="color: #888;">Public IP:</span> <code>{{.PublicIP}}</code></div>
                <div><span style="color: #888;">WG Gateway:</span> <code>{{.WGGatewayIP}}</code></div>
                <div>
                    <span style="color: #888;">IPv6:</span>
                    {{if .IPv6Status.Connectivity}}
                        <span class="status-ok">●</span> <code>{{.IPv6Status.PublicIPv6}}</code>
                    {{else}}
                        <span style="color: #f1c40f;">●</span> <span style="color: #f1c40f;">not detected</span>
                    {{end}}
                </div>
            </div>
            {{else}}
            <p class="status-err">Interface is DOWN</p>
            <form method="POST" action="/admin/interface/up" style="display:inline">
                <button class="success" type="submit">Bring Up</button>
            </form>
            {{end}}
        </div>

        <div class="card">
            <h2>Clients ({{len .Peers}})</h2>
            <p style="color: #888; font-size: 0.9em; margin-bottom: 1rem;">Admin clients can access this dashboard via their VPN IP without a token.</p>
            {{if .Peers}}
            <table>
                <thead><tr><th>Name</th><th>IP</th><th>Endpoint</th><th>Last Seen</th><th>Transfer</th><th>Admin</th><th></th></tr></thead>
                <tbody>
                {{range .Peers}}
                <tr>
                    <td data-label="Name">
                        {{if .Online}}<span class="status-ok">●</span>{{else}}<span class="status-err">●</span>{{end}}
                        {{if .Name}}{{.Name}}{{else}}<code>{{slice .PublicKey 0 8}}...</code>{{end}}
                    </td>
                    <td data-label="IP">{{.AllowedIPs}}</td>
                    <td data-label="Endpoint">{{if .Endpoint}}{{.Endpoint}}{{else}}-{{end}}</td>
                    <td data-label="Last Seen">{{if .LatestHandshake}}{{.LatestHandshake}}{{else}}never{{end}}</td>
                    <td data-label="Transfer">{{if .TransferRx}}↓{{.TransferRx}} ↑{{.TransferTx}}{{else}}-{{end}}</td>
                    <td data-label="Admin">
                        <form method="POST" action="/admin/client/toggle-admin" style="display:inline">
                            <input type="hidden" name="name" value="{{.Name}}">
                            {{if .IsAdmin}}
                            <button type="submit" class="success" style="padding: 0.25rem 0.5rem; font-size: 0.85em;" title="Click to revoke admin">✓</button>
                            {{else}}
                            <button type="submit" class="secondary" style="padding: 0.25rem 0.5rem; font-size: 0.85em;" title="Click to grant admin">-</button>
                            {{end}}
                        </form>
                    </td>
                    <td>
                        <button type="button" class="secondary" style="padding: 0.25rem 0.5rem; font-size: 0.85em;" onclick='showEditClient({{. | json}})'>Edit</button>
                        <form method="POST" action="/admin/client/delete" style="display:inline" onsubmit="return confirm('Remove this client?')">
                            <input type="hidden" name="pubkey" value="{{.PublicKey}}">
                            <button class="danger" type="submit">Remove</button>
                        </form>
                    </td>
                </tr>
                {{end}}
            </table>
            {{else}}
            <p>No clients configured yet.</p>
            {{end}}
        </div>

        <div class="card">
            <h2>Add Client</h2>
            <form method="POST" action="/admin/client">
                <input type="text" name="name" placeholder="Client name (e.g., johns-laptop)" required>
                <input type="text" name="extra_ips" placeholder="Extra allowed IPs (e.g., 192.168.1.0/24, 10.0.0.5/32)" style="margin-top: 0.5rem;">
                <p style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">Optional: Additional IP ranges this client can route traffic for.</p>
                <button type="submit">Generate & Add Client</button>
            </form>
        </div>

        <div class="card">
            <h2>Invites ({{len .Invites}})</h2>
            {{if .Invites}}
            {{range .Invites}}
            <div style="border: 1px solid #0f3460; border-radius: 8px; padding: 1rem; margin-bottom: 1rem;">
                <div style="display: flex; gap: 1rem; align-items: center;">
                    <div style="flex-grow: 1; min-width: 0;">
                        <div style="margin-bottom: 0.5rem;"><strong>Token:</strong> <code>{{.Token}}</code></div>
                        <div style="margin-bottom: 0.5rem; word-break: break-all;"><strong>URL:</strong> <a href="{{.URL}}" target="_blank">{{.URL}}</a></div>
                    </div>
                    <div class="flex">
                        <button type="button" class="secondary" onclick="showQR('{{.Token}}')">Show QR</button>
                        <form method="POST" action="/admin/invite/delete" style="display:inline">
                            <input type="hidden" name="token" value="{{.Token}}">
                            <button class="danger" type="submit">Revoke</button>
                        </form>
                    </div>
                </div>
                <div id="qr-{{.Token}}" style="display:none;">{{.QRCode}}</div>
            </div>
            {{end}}
            {{else}}
            <p>No active invites.</p>
            {{end}}
            <form method="POST" action="/admin/invite" style="margin-top: 1rem;">
                <button type="submit">Create Invite</button>
            </form>
        </div>

        <div class="card">
            <h2>DNS Zones ({{len .Zones}})</h2>
            <p style="color: #888; margin-bottom: 1rem;">Zones manage DNS hosted zones and wildcard SSL certificates.</p>
            <p style="color: #f1c40f; background: rgba(241,196,15,0.1); padding: 0.5rem; border-radius: 4px; margin-bottom: 1rem; font-size: 0.9em;">
                <strong>Tip:</strong> Add <code>vpn</code> and <code>*.vpn</code> as SSL SANs to support VPN-only services at <code>*.vpn.example.com</code>.
                Wildcard certs only cover one level, so <code>app.vpn.example.com</code> needs the <code>*.vpn</code> SAN.
            </p>
            {{if .Zones}}
            <table>
                <thead><tr><th>Zone</th><th>Zone ID</th><th>SSL</th><th>Sub-zones</th><th></th></tr></thead>
                <tbody>
                {{range .Zones}}
                <tr>
                    <td data-label="Zone"><strong>{{.Name}}</strong></td>
                    <td data-label="Zone ID"><code style="font-size: 0.85em;">{{.ZoneID}}</code></td>
                    <td data-label="SSL">
                        {{if .SSL}}
                            {{if .SSL.Enabled}}<span class="status-ok">Enabled</span>{{else}}<span style="color:#888">Disabled</span>{{end}}
                        {{else}}
                            <span style="color:#888">-</span>
                        {{end}}
                    </td>
                    <td data-label="SSL SANs">
                        {{if .SubZones}}
                            {{range .SubZones}}{{if eq . ""}}<code style="font-size: 0.85em; margin-right: 0.25rem;" title="Root domain">{{$.Name}}</code>{{else}}<code style="font-size: 0.85em; margin-right: 0.25rem;">{{.}}.{{$.Name}}</code>{{end}}{{end}}
                        {{else}}
                            <span style="color:#888">-</span>
                        {{end}}
                    </td>
                    <td>
                        <button type="button" class="secondary" style="padding: 0.25rem 0.5rem; font-size: 0.85em;" onclick='showEditZone({{. | json}})'>Edit</button>
                        <button type="button" class="secondary" style="padding: 0.25rem 0.5rem; font-size: 0.85em;" onclick="showAddSubZone('{{.Name}}')">+Sub</button>
                        <form method="POST" action="/admin/zone/delete" style="display:inline" onsubmit="return confirm('Delete zone {{.Name}}? This will also delete all services in this zone.')">
                            <input type="hidden" name="name" value="{{.Name}}">
                            <button class="danger" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Delete</button>
                        </form>
                    </td>
                </tr>
                {{end}}
                </tbody>
            </table>
            {{else}}
            <p>No zones configured. Add a zone to start managing services.</p>
            {{end}}
            <details style="margin-top: 1rem;">
                <summary style="cursor: pointer; font-weight: bold;">Add Zone</summary>
                <form method="POST" action="/admin/zone" style="margin-top: 0.5rem;" id="add-zone-form">
                    <label style="font-weight: bold; margin-bottom: 0.25rem; display: block;">DNS Provider</label>
                    <select name="dns_provider_type" id="dns-provider-type" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="toggleProviderFields(this.value)">
                        <option value="route53">AWS Route53</option>
                        <option value="cloudflare">Cloudflare</option>
                        <option value="digitalocean">DigitalOcean</option>
                        <option value="hetzner">Hetzner</option>
                        <option value="gandi">Gandi</option>
                        <option value="googlecloud">Google Cloud DNS</option>
                        <option value="duckdns">DuckDNS</option>
                        <option value="namecom">Name.com</option>
                    </select>

                    <!-- Route53 Fields -->
                    <div id="route53-fields">
                        <details style="margin-bottom: 0.5rem; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">
                            <summary style="cursor: pointer; color: #3498db; font-size: 0.9em;">How to set up AWS CLI for Route53</summary>
                            <ol style="padding-left: 1.5rem; margin: 0.5rem 0; color: #888; font-size: 0.85em; line-height: 1.6;">
                                <li>Install AWS CLI: <code>sudo snap install aws-cli --classic</code></li>
                                <li>Configure credentials: <code>aws configure</code></li>
                                <li>Enter your AWS Access Key ID and Secret Access Key</li>
                                <li>Profiles are stored in <code>~/.aws/credentials</code></li>
                            </ol>
                            <p style="color: #888; font-size: 0.85em; margin-top: 0.5rem;">
                                Or enter credentials manually below (not recommended for production).
                            </p>
                        </details>
                        <select name="aws_profile" id="zone-aws-profile" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="loadZonesForProfile(this.value)">
                            <option value="">Select AWS Profile (or enter credentials below)</option>
                            {{range .AWSProfiles}}
                            <option value="{{.}}">{{.}}</option>
                            {{end}}
                        </select>
                        <details style="margin-bottom: 0.5rem;">
                            <summary style="cursor: pointer; color: #888; font-size: 0.9em;">Or enter AWS credentials manually</summary>
                            <input type="text" name="aws_access_key_id" placeholder="AWS Access Key ID" style="margin-top: 0.5rem;">
                            <input type="password" name="aws_secret_access_key" placeholder="AWS Secret Access Key">
                            <input type="text" name="aws_region" placeholder="AWS Region (e.g., us-east-1)" value="us-east-1">
                        </details>
                        <select name="zone_select" id="zone-select" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="selectZone(this)" disabled>
                            <option value="">Select AWS Profile first</option>
                        </select>
                    </div>

                    <!-- Name.com Fields -->
                    <div id="namecom-fields" style="display: none;">
                        <details style="margin-bottom: 0.5rem; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">
                            <summary style="cursor: pointer; color: #3498db; font-size: 0.9em;">How to get Name.com API credentials</summary>
                            <ol style="padding-left: 1.5rem; margin: 0.5rem 0; color: #888; font-size: 0.85em; line-height: 1.6;">
                                <li>Enable API access at <a href="https://www.name.com/account/settings/security" target="_blank">name.com/account/settings/security</a></li>
                                <li>Create a token at <a href="https://www.name.com/account/settings/api" target="_blank">name.com/account/settings/api</a></li>
                                <li>Refresh the page, then copy the username and token</li>
                            </ol>
                        </details>
                        <input type="text" name="namecom_username" id="namecom-username" placeholder="Name.com Username (from API settings)">
                        <input type="password" name="namecom_api_token" id="namecom-api-token" placeholder="Name.com API Token">
                        <button type="button" onclick="loadNamecomDomains()" style="margin-bottom: 0.5rem;">Load Domains</button>
                        <select name="namecom_domain_select" id="namecom-domain-select" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="selectNamecomDomain(this)" disabled>
                            <option value="">Enter credentials and click Load Domains</option>
                        </select>
                    </div>

                    <!-- Cloudflare Fields -->
                    <div id="cloudflare-fields" style="display: none;">
                        <details style="margin-bottom: 0.5rem; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">
                            <summary style="cursor: pointer; color: #3498db; font-size: 0.9em;">How to get Cloudflare API Token</summary>
                            <ol style="padding-left: 1.5rem; margin: 0.5rem 0; color: #888; font-size: 0.85em; line-height: 1.6;">
                                <li>Go to <a href="https://dash.cloudflare.com/profile/api-tokens" target="_blank">Cloudflare API Tokens</a></li>
                                <li>Click "Create Token"</li>
                                <li>Use "Edit zone DNS" template or create custom with Zone:DNS:Edit permission</li>
                                <li>Copy the generated token (shown only once)</li>
                            </ol>
                        </details>
                        <input type="password" name="cloudflare_api_token" id="cloudflare-api-token" placeholder="Cloudflare API Token">
                        <button type="button" onclick="loadCloudflareZones()" style="margin-bottom: 0.5rem;">Load Zones</button>
                        <select name="cloudflare_zone_select" id="cloudflare-zone-select" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="selectCloudflareZone(this)" disabled>
                            <option value="">Enter API token and click Load Zones</option>
                        </select>
                    </div>

                    <!-- DigitalOcean Fields -->
                    <div id="digitalocean-fields" style="display: none;">
                        <details style="margin-bottom: 0.5rem; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">
                            <summary style="cursor: pointer; color: #3498db; font-size: 0.9em;">How to get DigitalOcean API Token</summary>
                            <ol style="padding-left: 1.5rem; margin: 0.5rem 0; color: #888; font-size: 0.85em; line-height: 1.6;">
                                <li>Go to <a href="https://cloud.digitalocean.com/account/api/tokens" target="_blank">DigitalOcean API Tokens</a></li>
                                <li>Click "Generate New Token"</li>
                                <li>Give it a name and select Read+Write scope</li>
                                <li>Copy the generated token (shown only once)</li>
                            </ol>
                        </details>
                        <input type="password" name="digitalocean_api_token" id="digitalocean-api-token" placeholder="DigitalOcean API Token">
                        <button type="button" onclick="loadDigitalOceanDomains()" style="margin-bottom: 0.5rem;">Load Domains</button>
                        <select name="digitalocean_domain_select" id="digitalocean-domain-select" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="selectDigitalOceanDomain(this)" disabled>
                            <option value="">Enter API token and click Load Domains</option>
                        </select>
                    </div>

                    <!-- Hetzner Fields -->
                    <div id="hetzner-fields" style="display: none;">
                        <details style="margin-bottom: 0.5rem; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">
                            <summary style="cursor: pointer; color: #3498db; font-size: 0.9em;">How to get Hetzner DNS API Token</summary>
                            <ol style="padding-left: 1.5rem; margin: 0.5rem 0; color: #888; font-size: 0.85em; line-height: 1.6;">
                                <li>Go to <a href="https://dns.hetzner.com/settings/api-token" target="_blank">Hetzner DNS Console</a></li>
                                <li>Click "Create access token"</li>
                                <li>Copy the generated token</li>
                            </ol>
                        </details>
                        <input type="password" name="hetzner_api_token" id="hetzner-api-token" placeholder="Hetzner DNS API Token">
                        <button type="button" onclick="loadHetznerZones()" style="margin-bottom: 0.5rem;">Load Zones</button>
                        <select name="hetzner_zone_select" id="hetzner-zone-select" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="selectHetznerZone(this)" disabled>
                            <option value="">Enter API token and click Load Zones</option>
                        </select>
                    </div>

                    <!-- Gandi Fields -->
                    <div id="gandi-fields" style="display: none;">
                        <details style="margin-bottom: 0.5rem; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">
                            <summary style="cursor: pointer; color: #3498db; font-size: 0.9em;">How to get Gandi API Token</summary>
                            <ol style="padding-left: 1.5rem; margin: 0.5rem 0; color: #888; font-size: 0.85em; line-height: 1.6;">
                                <li>Go to <a href="https://account.gandi.net/en/users/security" target="_blank">Gandi Account Security</a></li>
                                <li>Under "Personal Access Token", click "Create a token"</li>
                                <li>Give it permissions for "Manage domain name technical configurations"</li>
                                <li>Copy the generated token</li>
                            </ol>
                        </details>
                        <input type="password" name="gandi_api_token" id="gandi-api-token" placeholder="Gandi Personal Access Token">
                        <button type="button" onclick="loadGandiDomains()" style="margin-bottom: 0.5rem;">Load Domains</button>
                        <select name="gandi_domain_select" id="gandi-domain-select" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="selectGandiDomain(this)" disabled>
                            <option value="">Enter API token and click Load Domains</option>
                        </select>
                    </div>

                    <!-- Google Cloud DNS Fields -->
                    <div id="googlecloud-fields" style="display: none;">
                        <details style="margin-bottom: 0.5rem; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">
                            <summary style="cursor: pointer; color: #3498db; font-size: 0.9em;">How to set up Google Cloud DNS</summary>
                            <ol style="padding-left: 1.5rem; margin: 0.5rem 0; color: #888; font-size: 0.85em; line-height: 1.6;">
                                <li>Go to <a href="https://console.cloud.google.com/apis/credentials" target="_blank">Google Cloud Console</a></li>
                                <li>Create a Service Account with "DNS Administrator" role</li>
                                <li>Create and download a JSON key file</li>
                                <li>Enter your GCP project ID and paste the JSON key below</li>
                            </ol>
                        </details>
                        <input type="text" name="gcp_project" id="gcp-project" placeholder="GCP Project ID">
                        <textarea name="gcp_service_account_json" id="gcp-service-account-json" placeholder="Service Account JSON (paste entire key file)" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem; min-height: 100px; font-family: monospace; font-size: 0.85em;"></textarea>
                        <button type="button" onclick="loadGoogleCloudZones()" style="margin-bottom: 0.5rem;">Load Zones</button>
                        <select name="googlecloud_zone_select" id="googlecloud-zone-select" style="width: 100%; background: #0f3460; color: #fff; padding: 0.75rem; margin-bottom: 0.5rem;" onchange="selectGoogleCloudZone(this)" disabled>
                            <option value="">Enter project and credentials, then click Load Zones</option>
                        </select>
                    </div>

                    <!-- DuckDNS Fields -->
                    <div id="duckdns-fields" style="display: none;">
                        <details style="margin-bottom: 0.5rem; background: #1a1a2e; padding: 0.5rem; border-radius: 4px;">
                            <summary style="cursor: pointer; color: #3498db; font-size: 0.9em;">How to get DuckDNS Token</summary>
                            <ol style="padding-left: 1.5rem; margin: 0.5rem 0; color: #888; font-size: 0.85em; line-height: 1.6;">
                                <li>Go to <a href="https://www.duckdns.org/" target="_blank">DuckDNS</a> and sign in</li>
                                <li>Your token is displayed at the top of the page</li>
                                <li>DuckDNS only supports subdomains of duckdns.org</li>
                            </ol>
                        </details>
                        <input type="password" name="duckdns_api_token" id="duckdns-api-token" placeholder="DuckDNS Token">
                        <input type="text" name="duckdns_subdomain" id="duckdns-subdomain" placeholder="Your subdomain (e.g., mylab for mylab.duckdns.org)">
                        <p style="color: #888; font-size: 0.85em; margin-bottom: 0.5rem;">Zone will be set to: <code id="duckdns-zone-preview">subdomain.duckdns.org</code></p>
                    </div>

                    <div id="zone-error" class="error" style="display:none; margin-bottom: 0.5rem;"></div>
                    <input type="hidden" name="name" id="zone-name">
                    <input type="hidden" name="zone_id" id="zone-id">
                    <input type="text" name="ssl_email" placeholder="SSL Email (enables wildcard cert)">
                    <button type="submit">Add Zone</button>
                </form>
            </details>
        </div>

        <div class="card">
            <h2>Services ({{len .Services}})</h2>
            <p style="color: #888; margin-bottom: 1rem;">Configure DNS resolution and optional reverse proxy for your services.</p>
            {{if .TemplateZone}}
            <div style="background: rgba(52, 152, 219, 0.1); border: 1px solid #3498db; border-radius: 8px; padding: 1rem; margin-bottom: 1rem;">
                <h3 style="margin: 0 0 0.5rem 0; color: #3498db;">Quick Start: VPN Services Template</h3>
                <p style="color: #888; margin-bottom: 0.75rem;">Create standard VPN services for <strong>{{.TemplateZone}}</strong>:</p>
                <table style="font-size: 0.9em; margin-bottom: 0.75rem;">
                    <thead><tr><th>Service</th><th>Domain</th><th>Internal</th><th>External</th><th>Proxy</th></tr></thead>
                    <tbody>
                        <tr>
                            <td><strong>vpn</strong></td>
                            <td><code>vpn.{{.TemplateZone}}</code></td>
                            <td><code>{{.NATIP}}</code></td>
                            <td><code>{{.PublicIP}}</code></td>
                            <td>-</td>
                        </tr>
                        <tr>
                            <td><strong>vpn-kiosk</strong></td>
                            <td><code>kiosk.vpn.{{.TemplateZone}}</code></td>
                            <td><code>{{.NATIP}}</code></td>
                            <td><code>{{.PublicIP}}</code></td>
                            <td><code>localhost:8080</code></td>
                        </tr>
                        <tr>
                            <td><strong>vpn-admin</strong></td>
                            <td><code>admin.vpn.{{.TemplateZone}}</code></td>
                            <td><code>{{.WGGatewayIP}}</code></td>
                            <td>-</td>
                            <td><code>localhost:8080</code></td>
                        </tr>
                    </tbody>
                </table>
                <form method="POST" action="/admin/zone/template" style="display: inline;">
                    <input type="hidden" name="zone" value="{{.TemplateZone}}">
                    <button type="submit" class="success">Apply Template</button>
                </form>
                <span style="color: #888; font-size: 0.85em; margin-left: 0.5rem;">You can edit or remove services after applying.</span>
            </div>
            {{end}}
            {{if .Services}}
            <table>
                <thead><tr><th>Service</th><th>Domain</th><th>Internal</th><th>External</th><th>Proxy</th><th></th></tr></thead>
                <tbody>
                {{range $svc := .Services}}
                <tr>
                    <td data-label="Service"><strong>{{$svc.Name}}</strong></td>
                    <td data-label="Domain"><code>{{$svc.Domain}}</code></td>
                    <td data-label="Internal">
                        {{if $svc.InternalDNS}}
                            {{$expectedPrivate := $svc.InternalDNS.IP}}
                            {{$actualPrivate := index $.PrivateDNS $svc.Domain}}
                            {{if $actualPrivate}}
                                {{if eq $actualPrivate $expectedPrivate}}
                                    <span class="status-ok" title="Resolves correctly">⬤</span>
                                {{else}}
                                    <span class="status-err" title="Expected {{$expectedPrivate}}, got {{$actualPrivate}}">⬤</span>
                                {{end}}
                            {{else}}
                                <span class="status-err" title="Not resolving">⬤</span>
                            {{end}}
                            <code>{{$svc.InternalDNS.IP}}</code>
                        {{else}}
                            <span style="color:#666">-</span>
                        {{end}}
                    </td>
                    <td data-label="External">
                        {{if $svc.ExternalDNS}}
                            {{$actualPublic := index $.PublicDNS $svc.Domain}}
                            {{$expectedPublic := $svc.ExternalDNS.IP}}
                            {{if eq $expectedPublic ""}}{{$expectedPublic = $.Config.PublicIP}}{{end}}
                            {{if $actualPublic}}
                                {{if eq $actualPublic $expectedPublic}}
                                    <span class="status-ok" title="Resolves correctly">⬤</span>
                                {{else}}
                                    <span class="status-err" title="Expected {{$expectedPublic}}, got {{$actualPublic}}">⬤</span>
                                {{end}}
                                <code>{{$actualPublic}}</code>
                            {{else}}
                                <span class="status-err" title="Not resolving">⬤</span>
                                <span style="color:#666">pending</span>
                            {{end}}
                        {{else}}
                            <span style="color:#666">-</span>
                        {{end}}
                    </td>
                    <td data-label="Proxy">
                        {{if $svc.Proxy}}
                            <span class="status-ok" title="HAProxy backend">⬤</span>
                            <code style="font-size: 0.85em;">{{$svc.Proxy.Backend}}</code>
                        {{else}}
                            <span style="color:#666">-</span>
                        {{end}}
                    </td>
                    <td>
                        <button type="button" class="secondary" style="padding: 0.25rem 0.5rem; font-size: 0.85em;" onclick='showEditService({{$svc | json}})'>Edit</button>
                        <form method="POST" action="/admin/service/delete" style="display:inline" onsubmit="return confirm('Remove service {{$svc.Name}}?')">
                            <input type="hidden" name="name" value="{{$svc.Name}}">
                            <button class="danger" type="submit" style="padding: 0.25rem 0.5rem; font-size: 0.85em;">Remove</button>
                        </form>
                    </td>
                </tr>
                {{end}}
                </tbody>
            </table>
            {{else}}
            <p>No services configured. Add a zone first, then add services.</p>
            {{end}}
            {{if .Zones}}
            <details style="margin-top: 1rem;">
                <summary style="cursor: pointer; font-weight: bold;">Add Service</summary>
                <form method="POST" action="/admin/service" style="margin-top: 0.5rem;" id="add-service-form">
                    <div style="margin-bottom: 1rem; padding: 0.75rem; background: #1a1a2e; border-radius: 4px;">
                        <h4 style="margin: 0 0 0.5rem 0; color: #e94560;">Step 1: Basic Info</h4>
                        <input type="text" name="name" placeholder="Service name (e.g., grafana)" required>
                        <input type="text" name="domain" placeholder="grafana.example.com" required>
                        <p style="color: #888; font-size: 0.8em; margin: 0.25rem 0 0 0;">Supports wildcards (e.g., *.api.example.com) to route all subdomains to one backend.</p>
                    </div>

                    <div style="margin-bottom: 1rem; padding: 0.75rem; background: #1a1a2e; border-radius: 4px;">
                        <h4 style="margin: 0 0 0.5rem 0; color: #e94560;">Step 2: Internal DNS (VPN Clients)</h4>
                        <p style="color: #888; font-size: 0.85em; margin-bottom: 0.5rem;">What IP should VPN clients use to reach this service?</p>
                        <div style="margin-bottom: 0.5rem;">
                            <label style="display: flex; align-items: center; gap: 0.5rem;">
                                <input type="radio" name="internal_mode" value="direct" style="width: auto;" checked onchange="toggleInternalIP(this)">
                                Direct to service IP
                            </label>
                        </div>
                        <div style="margin-bottom: 0.5rem;">
                            <label style="display: flex; align-items: center; gap: 0.5rem;">
                                <input type="radio" name="internal_mode" value="haproxy" style="width: auto;" onchange="toggleInternalIP(this)">
                                Via this server (auto-detect IP)
                            </label>
                        </div>
                        <input type="text" name="internal_ip" id="add-internal-ip" placeholder="10.100.0.50">
                    </div>

                    <div style="margin-bottom: 1rem; padding: 0.75rem; background: #1a1a2e; border-radius: 4px;">
                        <h4 style="margin: 0 0 0.5rem 0; color: #e94560;">Step 3: External DNS (Public Internet)</h4>
                        <label style="display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem;">
                            <input type="checkbox" name="external_enabled" style="width: auto;" onchange="toggleExternal(this)">
                            Enable public access via Route53
                        </label>
                        <div id="add-external-opts" style="display: none; padding-left: 1rem; border-left: 2px solid #0f3460;">
                            <input type="text" name="external_ip" placeholder="Public IP (leave blank for auto-detect)">
                            <input type="number" name="ttl" placeholder="TTL" value="300" style="width: 100px;">
                        </div>
                    </div>

                    <div style="margin-bottom: 1rem; padding: 0.75rem; background: #1a1a2e; border-radius: 4px;">
                        <h4 style="margin: 0 0 0.5rem 0; color: #e94560;">Step 4: Reverse Proxy (HAProxy)</h4>
                        <label style="display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem;">
                            <input type="checkbox" name="proxy_enabled" style="width: auto;" onchange="toggleProxy(this)">
                            Enable HAProxy reverse proxy
                        </label>
                        <div id="add-proxy-opts" style="display: none; padding-left: 1rem; border-left: 2px solid #0f3460;">
                            <input type="text" name="proxy_backend" placeholder="Backend address (e.g., 10.100.0.50:3000)">
                            <input type="text" name="health_check" placeholder="Health check path (e.g., /api/health)">
                        </div>
                    </div>

                    <button type="submit">Add Service</button>
                </form>
            </details>
            {{end}}
            <button type="button" class="success" onclick="startSync()" style="margin-top: 0.5rem;">Sync All Services</button>
        </div>
    </div>

    <div id="qr-modal" class="modal-overlay" onclick="if(event.target===this)closeModal()">
        <div class="modal-content">
            <button class="modal-close" onclick="closeModal()">&times;</button>
            <div id="qr-modal-content" style="background: white; padding: 1rem; border-radius: 4px;"></div>
        </div>
    </div>

    <div id="iam-modal" class="modal-overlay" onclick="if(event.target===this)closeIAMModal()">
        <div class="modal-content" style="max-width: 700px;">
            <button class="modal-close" onclick="closeIAMModal()">&times;</button>
            <h2 style="margin-bottom: 1rem;">AWS Permissions Required</h2>
            <p style="margin-bottom: 1rem;">Your AWS profile doesn't have permission to list Route53 hosted zones. Add this IAM policy to your AWS user:</p>
            <pre id="iam-policy-content" style="max-height: 400px; overflow-y: auto; font-size: 0.85rem;"></pre>
            <p style="margin-top: 1rem; color: #888;">
                Go to AWS IAM Console → Users → Your User → Add inline policy → JSON tab → Paste the above policy
            </p>
        </div>
    </div>

    <div id="edit-service-modal" class="modal-overlay" onclick="if(event.target===this)closeEditServiceModal()">
        <div class="modal-content" style="max-width: 550px;">
            <button class="modal-close" onclick="closeEditServiceModal()">&times;</button>
            <h2 style="margin-bottom: 1rem;">Edit Service</h2>
            <form method="POST" action="/admin/service/edit" id="edit-service-form">
                <input type="hidden" name="original_name" id="edit-svc-original-name">

                <div style="margin-bottom: 1rem; padding: 0.5rem; background: #1a1a2e; border-radius: 4px;">
                    <strong style="color: #e94560;">Basic Info</strong>
                    <input type="text" name="name" id="edit-svc-name" placeholder="Service name" required style="margin-top: 0.5rem;">
                    <input type="text" name="domain" id="edit-svc-domain" placeholder="Domain" required>
                </div>

                <div style="margin-bottom: 1rem; padding: 0.5rem; background: #1a1a2e; border-radius: 4px;">
                    <strong style="color: #e94560;">Internal DNS</strong>
                    <input type="text" name="internal_ip" id="edit-svc-internal-ip" placeholder="Internal IP for VPN clients" style="margin-top: 0.5rem;">
                </div>

                <div style="margin-bottom: 1rem; padding: 0.5rem; background: #1a1a2e; border-radius: 4px;">
                    <strong style="color: #e94560;">External DNS</strong>
                    <label style="display: flex; align-items: center; gap: 0.5rem; margin: 0.5rem 0;">
                        <input type="checkbox" name="external_enabled" id="edit-svc-external" style="width: auto;" onchange="document.getElementById('edit-external-opts').style.display = this.checked ? 'block' : 'none'">
                        Enable public access
                    </label>
                    <div id="edit-external-opts" style="display: none; padding-left: 1rem; border-left: 2px solid #0f3460;">
                        <input type="text" name="external_ip" id="edit-svc-external-ip" placeholder="Public IP (blank = auto-detect)">
                        <input type="number" name="ttl" id="edit-svc-ttl" placeholder="TTL" value="300" style="width: 100px;">
                    </div>
                </div>

                <div style="margin-bottom: 1rem; padding: 0.5rem; background: #1a1a2e; border-radius: 4px;">
                    <strong style="color: #e94560;">Reverse Proxy</strong>
                    <label style="display: flex; align-items: center; gap: 0.5rem; margin: 0.5rem 0;">
                        <input type="checkbox" name="proxy_enabled" id="edit-svc-proxy" style="width: auto;" onchange="document.getElementById('edit-proxy-opts').style.display = this.checked ? 'block' : 'none'">
                        Enable HAProxy
                    </label>
                    <div id="edit-proxy-opts" style="display: none; padding-left: 1rem; border-left: 2px solid #0f3460;">
                        <input type="text" name="proxy_backend" id="edit-svc-proxy-backend" placeholder="Backend (e.g., 10.100.0.50:3000)">
                        <input type="text" name="health_check" id="edit-svc-health" placeholder="Health check path">
                    </div>
                </div>

                <button type="submit">Save Changes</button>
            </form>
        </div>
    </div>

    <div id="edit-zone-modal" class="modal-overlay" onclick="if(event.target===this)closeEditZoneModal()">
        <div class="modal-content" style="max-width: 450px;">
            <button class="modal-close" onclick="closeEditZoneModal()">&times;</button>
            <h2 style="margin-bottom: 1rem;">Edit Zone</h2>
            <form method="POST" action="/admin/zone/edit" id="edit-zone-form">
                <input type="hidden" name="original_name" id="edit-zone-name">
                <p style="color: #888; margin-bottom: 0.5rem;">Zone: <strong id="edit-zone-display"></strong></p>

                <label style="font-weight: bold; margin-top: 0.5rem; display: block;">SSL Certificate</label>
                <input type="text" name="ssl_email" id="edit-zone-ssl-email" placeholder="SSL Email (leave empty to disable SSL)">
                <p style="color: #888; font-size: 0.85em; margin-bottom: 1rem;">Leave email empty to disable SSL for this zone.</p>

                <label style="font-weight: bold; display: block;">SSL Certificate SANs</label>
                <p style="color: #888; font-size: 0.85em; margin-bottom: 0.5rem;">Domains included in the SSL certificate (wildcard *.zone is always included)</p>
                <div id="edit-zone-subzones" style="margin-bottom: 0.5rem;"></div>
                <div style="display: flex; gap: 0.5rem; margin-bottom: 0.5rem;">
                    <input type="text" id="edit-zone-new-subzone" placeholder="*.vpn or api" style="flex: 1;">
                    <button type="button" class="secondary" onclick="addEditSubZone()" style="margin: 0;">Add</button>
                </div>
                <div style="margin-bottom: 1rem;">
                    <button type="button" class="secondary" onclick="addEditRootDomain()" style="font-size: 0.85em;">Add Root Domain</button>
                </div>
                <input type="hidden" name="sub_zones" id="edit-zone-subzones-input">

                <button type="submit">Save Changes</button>
            </form>
        </div>
    </div>

    <div id="subzone-modal" class="modal-overlay" onclick="if(event.target===this)closeSubZoneModal()">
        <div class="modal-content" style="max-width: 400px;">
            <button class="modal-close" onclick="closeSubZoneModal()">&times;</button>
            <h2 style="margin-bottom: 1rem;">Add Sub-zone</h2>
            <p style="color: #888; margin-bottom: 1rem;">Add a sub-zone SAN to your certificate. Use "*." prefix for wildcards.</p>
            <form method="POST" action="/admin/zone/subzone" id="subzone-form">
                <input type="hidden" name="zone" id="subzone-zone">
                <div style="display: flex; align-items: center; gap: 0.5rem; margin-bottom: 1rem;">
                    <input type="text" name="subzone" id="subzone-name" placeholder="*.vpn or api" style="flex: 1;" pattern="(\*\.)?[a-z0-9-]+" required>
                    <code id="subzone-suffix">.example.com</code>
                </div>
                <p style="color: #888; font-size: 0.85em; margin-bottom: 1rem;">This will add <code id="subzone-preview">*.vpn.example.com</code> to your SSL certificate.</p>
                <button type="submit">Add Sub-zone</button>
            </form>
        </div>
    </div>

    <div id="sync-modal" class="modal-overlay" onclick="if(event.target===this && !syncRunning)closeSyncModal()">
        <div class="modal-content" style="width: 700px; max-width: 95%; max-height: 95%; display: flex; flex-direction: column;">
            <button class="modal-close" onclick="if(!syncRunning)closeSyncModal()" id="sync-close-btn">&times;</button>
            <h2 style="margin-bottom: 1rem; flex-shrink: 0;">
                <span id="sync-title-icon" class="loading" style="display: none;"></span>
                <span id="sync-title-text">Sync All Services</span>
            </h2>
            <div id="sync-log" style="background: #0a0a1a; padding: 1rem; border-radius: 4px; font-family: monospace; font-size: 0.9rem; flex: 1; overflow-y: auto; min-height: 100px;"></div>
            <div id="sync-actions" style="margin-top: 1rem; display: none; flex-shrink: 0;">
                <button type="button" class="secondary" onclick="closeSyncModal()">Close</button>
            </div>
        </div>
    </div>

    <div id="edit-client-modal" class="modal-overlay" onclick="if(event.target===this)closeEditClientModal()">
        <div class="modal-content" style="max-width: 500px;">
            <button class="modal-close" onclick="closeEditClientModal()">&times;</button>
            <h2 style="margin-bottom: 1rem;">Edit VPN Client</h2>
            <form method="POST" action="/admin/client/edit" id="edit-client-form">
                <input type="hidden" name="pubkey" id="edit-client-pubkey">

                <div style="margin-bottom: 1rem; padding: 0.5rem; background: #1a1a2e; border-radius: 4px;">
                    <strong style="color: #e94560;">Client Name</strong>
                    <input type="text" name="name" id="edit-client-name" placeholder="Client name" required style="margin-top: 0.5rem;">
                </div>

                <div style="margin-bottom: 1rem; padding: 0.5rem; background: #1a1a2e; border-radius: 4px;">
                    <strong style="color: #e94560;">Allowed IPs</strong>
                    <p style="color: #888; font-size: 0.85em; margin: 0.25rem 0;">Primary VPN IP (auto-assigned):</p>
                    <code id="edit-client-primary-ip" style="display: block; padding: 0.5rem; background: #0f3460; border-radius: 4px; margin-bottom: 0.5rem;"></code>
                    <p style="color: #888; font-size: 0.85em; margin: 0.25rem 0;">Extra allowed IPs (comma-separated):</p>
                    <input type="text" name="extra_ips" id="edit-client-extra-ips" placeholder="e.g., 192.168.1.0/24, 10.0.0.5/32" style="margin-top: 0.25rem;">
                    <p style="color: #888; font-size: 0.85em; margin-top: 0.25rem;">Additional IP ranges this client can route traffic for.</p>
                </div>

                <button type="submit">Save Changes</button>
            </form>
        </div>
    </div>

    <script>
    function showQR(token) {
        var qr = document.getElementById('qr-' + token);
        if (qr) {
            document.getElementById('qr-modal-content').innerHTML = qr.innerHTML;
            document.getElementById('qr-modal').classList.add('active');
        }
    }
    function closeModal() {
        document.getElementById('qr-modal').classList.remove('active');
    }
    document.addEventListener('keydown', function(e) {
        if (e.key === 'Escape') closeModal();
    });

    var iamPolicy = ` + "`" + `{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "Route53ListZones",
            "Effect": "Allow",
            "Action": [
                "route53:ListHostedZones",
                "route53:ListHostedZonesByName"
            ],
            "Resource": "*"
        },
        {
            "Sid": "Route53ManageRecords",
            "Effect": "Allow",
            "Action": [
                "route53:GetHostedZone",
                "route53:ListResourceRecordSets",
                "route53:ChangeResourceRecordSets",
                "route53:GetChange"
            ],
            "Resource": [
                "arn:aws:route53:::hostedzone/*"
            ]
        },
        {
            "Sid": "Route53GetChanges",
            "Effect": "Allow",
            "Action": [
                "route53:GetChange"
            ],
            "Resource": "arn:aws:route53:::change/*"
        }
    ]
}` + "`" + `;

    function showIAMModal() {
        document.getElementById('iam-policy-content').textContent = iamPolicy;
        document.getElementById('iam-modal').classList.add('active');
    }

    function closeIAMModal() {
        document.getElementById('iam-modal').classList.remove('active');
    }

    function loadZonesForProfile(profile) {
        var zoneSelect = document.getElementById('zone-select');
        var errorDiv = document.getElementById('zone-error');
        var zoneName = document.getElementById('zone-name');
        var zoneId = document.getElementById('zone-id');

        errorDiv.style.display = 'none';
        zoneName.value = '';
        zoneId.value = '';

        if (!profile) {
            zoneSelect.disabled = true;
            zoneSelect.innerHTML = '<option value="">Select AWS Profile first</option>';
            return;
        }

        zoneSelect.disabled = true;
        zoneSelect.innerHTML = '<option value="">Loading zones...</option>';

        fetch('/admin/dns/discover-zones?provider=route53&aws_profile=' + encodeURIComponent(profile))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                zoneSelect.disabled = false;
                if (data.error) {
                    // Check if it's a permission error
                    if (data.error.indexOf('AccessDenied') !== -1 ||
                        data.error.indexOf('not authorized') !== -1 ||
                        data.error.indexOf('credentials') !== -1) {
                        showIAMModal();
                    }
                    errorDiv.innerHTML = 'Error: ' + data.error + ' <a href="#" onclick="showIAMModal(); return false;">View required permissions</a>';
                    errorDiv.style.display = 'block';
                    zoneSelect.innerHTML = '<option value="">Failed to load zones</option>';
                    return;
                }
                if (!data.zones || data.zones.length === 0) {
                    zoneSelect.innerHTML = '<option value="">No hosted zones found</option>';
                    return;
                }
                var html = '<option value="">Select Route53 Zone</option>';
                data.zones.forEach(function(z) {
                    html += '<option value="' + z.ID + '" data-name="' + z.Name + '">' + z.Name + ' (' + z.ID + ')</option>';
                });
                zoneSelect.innerHTML = html;
            })
            .catch(function(err) {
                zoneSelect.disabled = false;
                errorDiv.innerHTML = 'Error: ' + err.message + ' <a href="#" onclick="showIAMModal(); return false;">View required permissions</a>';
                errorDiv.style.display = 'block';
                zoneSelect.innerHTML = '<option value="">Failed to load zones</option>';
            });
    }

    function selectZone(select) {
        var option = select.options[select.selectedIndex];
        document.getElementById('zone-name').value = option.dataset.name || '';
        document.getElementById('zone-id').value = option.value || '';
    }

    function toggleProviderFields(provider) {
        var zoneName = document.getElementById('zone-name');
        var zoneId = document.getElementById('zone-id');

        // Reset hidden fields
        zoneName.value = '';
        zoneId.value = '';

        // Hide all provider fields first
        var allFields = ['route53', 'namecom', 'cloudflare', 'digitalocean', 'hetzner', 'gandi', 'googlecloud', 'duckdns'];
        allFields.forEach(function(p) {
            var el = document.getElementById(p + '-fields');
            if (el) el.style.display = 'none';
        });

        // Show selected provider fields
        var selected = document.getElementById(provider + '-fields');
        if (selected) selected.style.display = 'block';
    }

    function loadNamecomDomains() {
        var username = document.getElementById('namecom-username').value;
        var apiToken = document.getElementById('namecom-api-token').value;
        var domainSelect = document.getElementById('namecom-domain-select');
        var errorDiv = document.getElementById('zone-error');

        errorDiv.style.display = 'none';

        if (!username || !apiToken) {
            errorDiv.innerHTML = 'Please enter Name.com username and API token';
            errorDiv.style.display = 'block';
            return;
        }

        domainSelect.disabled = true;
        domainSelect.innerHTML = '<option value="">Loading domains...</option>';

        fetch('/admin/dns/discover-zones?provider=namecom&username=' + encodeURIComponent(username) + '&api_token=' + encodeURIComponent(apiToken))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                domainSelect.disabled = false;
                if (data.error) {
                    errorDiv.innerHTML = 'Error: ' + data.error;
                    errorDiv.style.display = 'block';
                    domainSelect.innerHTML = '<option value="">Failed to load domains</option>';
                    return;
                }
                if (!data.zones || data.zones.length === 0) {
                    domainSelect.innerHTML = '<option value="">No domains found</option>';
                    return;
                }
                var html = '<option value="">Select Domain</option>';
                data.zones.forEach(function(z) {
                    var warning = z.DNSManaged ? '' : ' (DNS elsewhere)';
                    html += '<option value="' + z.ID + '" data-name="' + z.Name + '" data-managed="' + z.DNSManaged + '">' + z.Name + warning + '</option>';
                });
                domainSelect.innerHTML = html;
            })
            .catch(function(err) {
                domainSelect.disabled = false;
                errorDiv.innerHTML = 'Error: ' + err.message;
                errorDiv.style.display = 'block';
                domainSelect.innerHTML = '<option value="">Failed to load domains</option>';
            });
    }

    function selectNamecomDomain(select) {
        var option = select.options[select.selectedIndex];
        var errorDiv = document.getElementById('zone-error');
        document.getElementById('zone-name').value = option.dataset.name || '';
        document.getElementById('zone-id').value = option.value || '';

        // Show warning if DNS is managed elsewhere
        if (option.dataset.managed === 'false') {
            errorDiv.innerHTML = '<strong>Warning:</strong> DNS for this domain is managed by external nameservers. DNS records created here will not work until you switch to Name.com nameservers or use the correct provider.';
            errorDiv.style.display = 'block';
            errorDiv.style.background = '#f39c12';
        } else {
            errorDiv.style.display = 'none';
        }
    }

    function loadCloudflareZones() {
        var apiToken = document.getElementById('cloudflare-api-token').value;
        var zoneSelect = document.getElementById('cloudflare-zone-select');
        var errorDiv = document.getElementById('zone-error');

        errorDiv.style.display = 'none';

        if (!apiToken) {
            errorDiv.innerHTML = 'Please enter Cloudflare API token';
            errorDiv.style.display = 'block';
            return;
        }

        zoneSelect.disabled = true;
        zoneSelect.innerHTML = '<option value="">Loading zones...</option>';

        fetch('/admin/dns/discover-zones?provider=cloudflare&api_token=' + encodeURIComponent(apiToken))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                zoneSelect.disabled = false;
                if (data.error) {
                    errorDiv.innerHTML = 'Error: ' + data.error;
                    errorDiv.style.display = 'block';
                    zoneSelect.innerHTML = '<option value="">Failed to load zones</option>';
                    return;
                }
                if (!data.zones || data.zones.length === 0) {
                    zoneSelect.innerHTML = '<option value="">No zones found</option>';
                    return;
                }
                var html = '<option value="">Select Zone</option>';
                data.zones.forEach(function(z) {
                    var status = z.DNSManaged ? '' : ' (inactive)';
                    html += '<option value="' + z.ID + '" data-name="' + z.Name + '" data-managed="' + z.DNSManaged + '">' + z.Name + status + '</option>';
                });
                zoneSelect.innerHTML = html;
            })
            .catch(function(err) {
                zoneSelect.disabled = false;
                errorDiv.innerHTML = 'Error: ' + err.message;
                errorDiv.style.display = 'block';
                zoneSelect.innerHTML = '<option value="">Failed to load zones</option>';
            });
    }

    function selectCloudflareZone(select) {
        var option = select.options[select.selectedIndex];
        var errorDiv = document.getElementById('zone-error');
        document.getElementById('zone-name').value = option.dataset.name || '';
        document.getElementById('zone-id').value = option.value || '';

        // Show warning if zone is not active
        if (option.dataset.managed === 'false') {
            errorDiv.innerHTML = '<strong>Warning:</strong> This zone is not active on Cloudflare. DNS records may not work.';
            errorDiv.style.display = 'block';
            errorDiv.style.background = '#f39c12';
        } else {
            errorDiv.style.display = 'none';
        }
    }

    // DigitalOcean functions
    function loadDigitalOceanDomains() {
        var apiToken = document.getElementById('digitalocean-api-token').value;
        var domainSelect = document.getElementById('digitalocean-domain-select');
        var errorDiv = document.getElementById('zone-error');

        errorDiv.style.display = 'none';

        if (!apiToken) {
            errorDiv.innerHTML = 'Please enter DigitalOcean API token';
            errorDiv.style.display = 'block';
            return;
        }

        domainSelect.disabled = true;
        domainSelect.innerHTML = '<option value="">Loading domains...</option>';

        fetch('/admin/dns/discover-zones?provider=digitalocean&api_token=' + encodeURIComponent(apiToken))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                domainSelect.disabled = false;
                if (data.error) {
                    errorDiv.innerHTML = 'Error: ' + data.error;
                    errorDiv.style.display = 'block';
                    domainSelect.innerHTML = '<option value="">Failed to load domains</option>';
                    return;
                }
                if (!data.zones || data.zones.length === 0) {
                    domainSelect.innerHTML = '<option value="">No domains found</option>';
                    return;
                }
                var html = '<option value="">Select Domain</option>';
                data.zones.forEach(function(z) {
                    html += '<option value="' + z.ID + '" data-name="' + z.Name + '">' + z.Name + '</option>';
                });
                domainSelect.innerHTML = html;
            })
            .catch(function(err) {
                domainSelect.disabled = false;
                errorDiv.innerHTML = 'Error: ' + err.message;
                errorDiv.style.display = 'block';
                domainSelect.innerHTML = '<option value="">Failed to load domains</option>';
            });
    }

    function selectDigitalOceanDomain(select) {
        var option = select.options[select.selectedIndex];
        document.getElementById('zone-name').value = option.dataset.name || '';
        document.getElementById('zone-id').value = option.value || '';
    }

    // Hetzner functions
    function loadHetznerZones() {
        var apiToken = document.getElementById('hetzner-api-token').value;
        var zoneSelect = document.getElementById('hetzner-zone-select');
        var errorDiv = document.getElementById('zone-error');

        errorDiv.style.display = 'none';

        if (!apiToken) {
            errorDiv.innerHTML = 'Please enter Hetzner DNS API token';
            errorDiv.style.display = 'block';
            return;
        }

        zoneSelect.disabled = true;
        zoneSelect.innerHTML = '<option value="">Loading zones...</option>';

        fetch('/admin/dns/discover-zones?provider=hetzner&api_token=' + encodeURIComponent(apiToken))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                zoneSelect.disabled = false;
                if (data.error) {
                    errorDiv.innerHTML = 'Error: ' + data.error;
                    errorDiv.style.display = 'block';
                    zoneSelect.innerHTML = '<option value="">Failed to load zones</option>';
                    return;
                }
                if (!data.zones || data.zones.length === 0) {
                    zoneSelect.innerHTML = '<option value="">No zones found</option>';
                    return;
                }
                var html = '<option value="">Select Zone</option>';
                data.zones.forEach(function(z) {
                    html += '<option value="' + z.ID + '" data-name="' + z.Name + '">' + z.Name + '</option>';
                });
                zoneSelect.innerHTML = html;
            })
            .catch(function(err) {
                zoneSelect.disabled = false;
                errorDiv.innerHTML = 'Error: ' + err.message;
                errorDiv.style.display = 'block';
                zoneSelect.innerHTML = '<option value="">Failed to load zones</option>';
            });
    }

    function selectHetznerZone(select) {
        var option = select.options[select.selectedIndex];
        document.getElementById('zone-name').value = option.dataset.name || '';
        document.getElementById('zone-id').value = option.value || '';
    }

    // Gandi functions
    function loadGandiDomains() {
        var apiToken = document.getElementById('gandi-api-token').value;
        var domainSelect = document.getElementById('gandi-domain-select');
        var errorDiv = document.getElementById('zone-error');

        errorDiv.style.display = 'none';

        if (!apiToken) {
            errorDiv.innerHTML = 'Please enter Gandi API token';
            errorDiv.style.display = 'block';
            return;
        }

        domainSelect.disabled = true;
        domainSelect.innerHTML = '<option value="">Loading domains...</option>';

        fetch('/admin/dns/discover-zones?provider=gandi&api_token=' + encodeURIComponent(apiToken))
            .then(function(r) { return r.json(); })
            .then(function(data) {
                domainSelect.disabled = false;
                if (data.error) {
                    errorDiv.innerHTML = 'Error: ' + data.error;
                    errorDiv.style.display = 'block';
                    domainSelect.innerHTML = '<option value="">Failed to load domains</option>';
                    return;
                }
                if (!data.zones || data.zones.length === 0) {
                    domainSelect.innerHTML = '<option value="">No domains found</option>';
                    return;
                }
                var html = '<option value="">Select Domain</option>';
                data.zones.forEach(function(z) {
                    html += '<option value="' + z.ID + '" data-name="' + z.Name + '">' + z.Name + '</option>';
                });
                domainSelect.innerHTML = html;
            })
            .catch(function(err) {
                domainSelect.disabled = false;
                errorDiv.innerHTML = 'Error: ' + err.message;
                errorDiv.style.display = 'block';
                domainSelect.innerHTML = '<option value="">Failed to load domains</option>';
            });
    }

    function selectGandiDomain(select) {
        var option = select.options[select.selectedIndex];
        document.getElementById('zone-name').value = option.dataset.name || '';
        document.getElementById('zone-id').value = option.value || '';
    }

    // Google Cloud DNS functions
    function loadGoogleCloudZones() {
        var project = document.getElementById('gcp-project').value;
        var saJson = document.getElementById('gcp-service-account-json').value;
        var zoneSelect = document.getElementById('googlecloud-zone-select');
        var errorDiv = document.getElementById('zone-error');

        errorDiv.style.display = 'none';

        if (!project) {
            errorDiv.innerHTML = 'Please enter GCP Project ID';
            errorDiv.style.display = 'block';
            return;
        }

        zoneSelect.disabled = true;
        zoneSelect.innerHTML = '<option value="">Loading zones...</option>';

        var url = '/admin/dns/discover-zones?provider=googlecloud&gcp_project=' + encodeURIComponent(project);
        if (saJson) {
            url += '&gcp_service_account_json=' + encodeURIComponent(saJson);
        }

        fetch(url)
            .then(function(r) { return r.json(); })
            .then(function(data) {
                zoneSelect.disabled = false;
                if (data.error) {
                    errorDiv.innerHTML = 'Error: ' + data.error;
                    errorDiv.style.display = 'block';
                    zoneSelect.innerHTML = '<option value="">Failed to load zones</option>';
                    return;
                }
                if (!data.zones || data.zones.length === 0) {
                    zoneSelect.innerHTML = '<option value="">No zones found</option>';
                    return;
                }
                var html = '<option value="">Select Zone</option>';
                data.zones.forEach(function(z) {
                    html += '<option value="' + z.ID + '" data-name="' + z.Name + '">' + z.Name + '</option>';
                });
                zoneSelect.innerHTML = html;
            })
            .catch(function(err) {
                zoneSelect.disabled = false;
                errorDiv.innerHTML = 'Error: ' + err.message;
                errorDiv.style.display = 'block';
                zoneSelect.innerHTML = '<option value="">Failed to load zones</option>';
            });
    }

    function selectGoogleCloudZone(select) {
        var option = select.options[select.selectedIndex];
        document.getElementById('zone-name').value = option.dataset.name || '';
        document.getElementById('zone-id').value = option.value || '';
    }

    // DuckDNS functions
    document.addEventListener('DOMContentLoaded', function() {
        var duckdnsSubdomain = document.getElementById('duckdns-subdomain');
        if (duckdnsSubdomain) {
            duckdnsSubdomain.addEventListener('input', function() {
                var preview = document.getElementById('duckdns-zone-preview');
                var subdomain = this.value.trim() || 'subdomain';
                preview.textContent = subdomain + '.duckdns.org';
                // Auto-set zone name and ID for DuckDNS
                document.getElementById('zone-name').value = subdomain + '.duckdns.org';
                document.getElementById('zone-id').value = subdomain;
            });
        }
    });

    var syncRunning = false;
    var syncEventSource = null;

    var levelColors = {
        'info': '#888',
        'success': '#2ecc71',
        'warning': '#f1c40f',
        'error': '#e74c3c',
        'step': '#3498db'
    };

    function appendSyncLog(data) {
        var log = document.getElementById('sync-log');
        var line = document.createElement('div');
        line.style.marginBottom = '0.25rem';

        var color = levelColors[data.level] || '#fff';
        var prefix = '';

        if (data.level === 'step') {
            prefix = '▶ ';
            line.style.fontWeight = 'bold';
            line.style.marginTop = '0.5rem';
        } else if (data.level === 'success') {
            prefix = '✓ ';
        } else if (data.level === 'error') {
            prefix = '✗ ';
        } else if (data.level === 'warning') {
            prefix = '⚠ ';
        }

        line.style.color = color;
        line.textContent = prefix + data.message;
        log.appendChild(line);
        log.scrollTop = log.scrollHeight;
    }

    function setSyncComplete(status) {
        var titleIcon = document.getElementById('sync-title-icon');
        var titleText = document.getElementById('sync-title-text');
        var closeBtn = document.getElementById('sync-close-btn');
        var actions = document.getElementById('sync-actions');

        syncRunning = false;
        titleIcon.style.display = 'none';
        closeBtn.style.opacity = '1';
        actions.style.display = 'block';

        if (status === 'success') {
            titleText.textContent = 'Sync Complete';
        } else {
            titleText.textContent = 'Sync Completed with Errors';
        }
    }

    function connectToSync() {
        if (syncEventSource) {
            syncEventSource.close();
        }

        syncEventSource = new EventSource('/admin/services/sync');

        syncEventSource.onmessage = function(event) {
            var data = JSON.parse(event.data);

            if (data.done) {
                setSyncComplete(data.status);
                syncEventSource.close();
                syncEventSource = null;
                return;
            }

            appendSyncLog(data);
        };

        syncEventSource.onerror = function() {
            setSyncComplete('failed');
            appendSyncLog({level: 'error', message: 'Connection error'});
            syncEventSource.close();
            syncEventSource = null;
        };
    }

    function startSync() {
        var modal = document.getElementById('sync-modal');
        var log = document.getElementById('sync-log');
        var actions = document.getElementById('sync-actions');
        var titleIcon = document.getElementById('sync-title-icon');
        var titleText = document.getElementById('sync-title-text');
        var closeBtn = document.getElementById('sync-close-btn');

        // Reset state
        log.innerHTML = '';
        actions.style.display = 'none';
        titleIcon.style.display = 'inline-block';
        titleText.textContent = 'Syncing Services...';
        closeBtn.style.opacity = '0.5';
        syncRunning = true;
        modal.classList.add('active');

        connectToSync();
    }

    function closeSyncModal() {
        if (syncRunning) return;
        document.getElementById('sync-modal').classList.remove('active');
        if (syncEventSource) {
            syncEventSource.close();
            syncEventSource = null;
        }
    }

    // Check if a sync is already running on page load
    function checkSyncStatus() {
        fetch('/admin/services/sync/status')
            .then(function(response) { return response.json(); })
            .then(function(data) {
                if (data.running) {
                    // A sync is running - show modal, replay history, and connect
                    var modal = document.getElementById('sync-modal');
                    var log = document.getElementById('sync-log');
                    var actions = document.getElementById('sync-actions');
                    var titleIcon = document.getElementById('sync-title-icon');
                    var titleText = document.getElementById('sync-title-text');
                    var closeBtn = document.getElementById('sync-close-btn');

                    log.innerHTML = '';
                    actions.style.display = 'none';
                    titleIcon.style.display = 'inline-block';
                    titleText.textContent = 'Syncing Services...';
                    closeBtn.style.opacity = '0.5';
                    syncRunning = true;
                    modal.classList.add('active');

                    // Replay history
                    if (data.history) {
                        data.history.forEach(function(jsonMsg) {
                            var msg = JSON.parse(jsonMsg);
                            if (msg.done) {
                                setSyncComplete(msg.status);
                            } else {
                                appendSyncLog(msg);
                            }
                        });
                    }

                    // Connect to continue receiving updates
                    connectToSync();
                }
            })
            .catch(function(err) {
                console.log('Could not check sync status:', err);
            });
    }

    // Check sync status when page loads
    checkSyncStatus();

    function showAddSubZone(zoneName) {
        document.getElementById('subzone-zone').value = zoneName;
        document.getElementById('subzone-suffix').textContent = '.' + zoneName;
        document.getElementById('subzone-name').value = '';
        updateSubZonePreview();
        document.getElementById('subzone-modal').classList.add('active');
        document.getElementById('subzone-name').focus();
    }

    function closeSubZoneModal() {
        document.getElementById('subzone-modal').classList.remove('active');
    }

    function updateSubZonePreview() {
        var zone = document.getElementById('subzone-zone').value;
        var subzone = document.getElementById('subzone-name').value || '*.vpn';
        document.getElementById('subzone-preview').textContent = subzone + '.' + zone;
    }

    document.addEventListener('DOMContentLoaded', function() {
        var subzoneInput = document.getElementById('subzone-name');
        if (subzoneInput) {
            subzoneInput.addEventListener('input', updateSubZonePreview);
        }
    });

    // Add Service form toggle functions
    function toggleInternalIP(radio) {
        var ipInput = document.getElementById('add-internal-ip');
        if (radio.value === 'haproxy') {
            ipInput.style.display = 'none';
            ipInput.value = '';
        } else {
            ipInput.style.display = 'block';
        }
    }

    function toggleExternal(checkbox) {
        document.getElementById('add-external-opts').style.display = checkbox.checked ? 'block' : 'none';
    }

    function toggleProxy(checkbox) {
        document.getElementById('add-proxy-opts').style.display = checkbox.checked ? 'block' : 'none';
        // If enabling proxy, suggest setting internal DNS to haproxy mode
        if (checkbox.checked) {
            var haproxyRadio = document.querySelector('input[name="internal_mode"][value="haproxy"]');
            if (haproxyRadio) {
                haproxyRadio.checked = true;
                toggleInternalIP(haproxyRadio);
            }
        }
    }

    function showEditService(svc) {
        document.getElementById('edit-svc-original-name').value = svc.name;
        document.getElementById('edit-svc-name').value = svc.name;
        document.getElementById('edit-svc-domain').value = svc.domain;

        // Internal DNS
        document.getElementById('edit-svc-internal-ip').value = (svc.internal_dns && svc.internal_dns.ip) || '';

        // External DNS
        var hasExternal = svc.external_dns != null;
        document.getElementById('edit-svc-external').checked = hasExternal;
        document.getElementById('edit-external-opts').style.display = hasExternal ? 'block' : 'none';
        if (hasExternal) {
            document.getElementById('edit-svc-external-ip').value = svc.external_dns.ip || '';
            document.getElementById('edit-svc-ttl').value = svc.external_dns.ttl || 300;
        } else {
            document.getElementById('edit-svc-external-ip').value = '';
            document.getElementById('edit-svc-ttl').value = 300;
        }

        // Proxy
        var hasProxy = svc.proxy != null;
        document.getElementById('edit-svc-proxy').checked = hasProxy;
        document.getElementById('edit-proxy-opts').style.display = hasProxy ? 'block' : 'none';
        if (hasProxy) {
            document.getElementById('edit-svc-proxy-backend').value = svc.proxy.backend || '';
            document.getElementById('edit-svc-health').value = (svc.proxy.health_check && svc.proxy.health_check.path) || '';
        } else {
            document.getElementById('edit-svc-proxy-backend').value = '';
            document.getElementById('edit-svc-health').value = '';
        }

        document.getElementById('edit-service-modal').classList.add('active');
    }

    function closeEditServiceModal() {
        document.getElementById('edit-service-modal').classList.remove('active');
    }

    var editZoneSubZones = [];
    var editZoneName = '';

    function showEditZone(zone) {
        editZoneName = zone.name;
        editZoneSubZones = (zone.sub_zones || []).slice();
        document.getElementById('edit-zone-name').value = zone.name;
        document.getElementById('edit-zone-display').textContent = zone.name;
        document.getElementById('edit-zone-ssl-email').value = (zone.ssl && zone.ssl.email) || '';
        renderEditSubZones();
        document.getElementById('edit-zone-modal').classList.add('active');
    }

    function renderEditSubZones() {
        var container = document.getElementById('edit-zone-subzones');
        var input = document.getElementById('edit-zone-subzones-input');
        if (editZoneSubZones.length === 0) {
            container.innerHTML = '<p style="color: #666; font-style: italic; margin: 0.5rem 0;">No SANs configured (only *.zone will be included)</p>';
        } else {
            container.innerHTML = editZoneSubZones.map(function(sz, idx) {
                var displayName = sz === '' ? editZoneName + ' <span style="color:#888">(root)</span>' : sz + '.' + editZoneName;
                return '<div style="display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.25rem; padding: 0.25rem 0.5rem; background: #0f3460; border-radius: 4px;">' +
                    '<code style="flex: 1;">' + displayName + '</code>' +
                    '<button type="button" class="danger" style="padding: 0.2rem 0.5rem; font-size: 0.8em; margin: 0;" onclick="removeEditSubZone(' + idx + ')">×</button>' +
                    '</div>';
            }).join('');
        }
        input.value = editZoneSubZones.join(',');
    }

    function addEditSubZone() {
        var input = document.getElementById('edit-zone-new-subzone');
        var val = input.value.trim().toLowerCase();
        if (val && /^(\*\.)?[a-z0-9-]+$/.test(val) && editZoneSubZones.indexOf(val) === -1) {
            editZoneSubZones.push(val);
            renderEditSubZones();
            input.value = '';
        }
    }

    function addEditRootDomain() {
        if (editZoneSubZones.indexOf('') === -1) {
            editZoneSubZones.push('');
            renderEditSubZones();
        }
    }

    function removeEditSubZone(idx) {
        editZoneSubZones.splice(idx, 1);
        renderEditSubZones();
    }

    function closeEditZoneModal() {
        document.getElementById('edit-zone-modal').classList.remove('active');
    }

    function showEditClient(client) {
        document.getElementById('edit-client-pubkey').value = client.PublicKey;
        document.getElementById('edit-client-name').value = client.Name || '';

        // Parse AllowedIPs to separate primary IP from extra IPs
        var allowedIPs = client.AllowedIPs || '';
        var ips = allowedIPs.split(',').map(function(ip) { return ip.trim(); }).filter(function(ip) { return ip; });

        // First IP is the primary VPN IP (the /32 assigned by the system)
        var primaryIP = '';
        var extraIPs = [];

        for (var i = 0; i < ips.length; i++) {
            if (ips[i].endsWith('/32') && primaryIP === '') {
                primaryIP = ips[i];
            } else {
                extraIPs.push(ips[i]);
            }
        }

        document.getElementById('edit-client-primary-ip').textContent = primaryIP || allowedIPs;
        document.getElementById('edit-client-extra-ips').value = extraIPs.join(', ');

        document.getElementById('edit-client-modal').classList.add('active');
    }

    function closeEditClientModal() {
        document.getElementById('edit-client-modal').classList.remove('active');
    }

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
    <footer style="text-align: center; padding: 2rem 0 1rem; color: #555; font-size: 0.85em;">
        <span>Homelab Horizon {{.Version}}</span>
        <a href="https://github.com/IodeSystems/homelab-horizon" target="_blank" title="View on GitHub" style="margin-left: 0.5rem; color: #555; vertical-align: middle;">
            <svg height="16" width="16" viewBox="0 0 16 16" fill="currentColor" style="vertical-align: text-bottom;"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
        </a>
    </footer>
</body>
</html>`
