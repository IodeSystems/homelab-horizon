package server

const helpTemplate = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Getting Started - Homelab Horizon</title>
    <style>` + baseCSS + `
    .step {
        background: #0f3460;
        border-left: 4px solid #e94560;
        padding: 1rem;
        margin-bottom: 1rem;
        border-radius: 0 8px 8px 0;
    }
    .step-number {
        display: inline-block;
        background: #e94560;
        color: white;
        width: 2rem;
        height: 2rem;
        border-radius: 50%;
        text-align: center;
        line-height: 2rem;
        font-weight: bold;
        margin-right: 0.75rem;
    }
    .step h3 { display: inline; vertical-align: middle; }
    .step-content { margin-top: 0.75rem; padding-left: 2.75rem; }
    .tip { background: rgba(46, 204, 113, 0.1); border-left: 3px solid #2ecc71; padding: 0.5rem 1rem; margin: 0.5rem 0; }
    .warning { background: rgba(241, 196, 15, 0.1); border-left: 3px solid #f1c40f; padding: 0.5rem 1rem; margin: 0.5rem 0; }
    .feature-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 1rem; margin: 1rem 0; }
    .feature { background: #16213e; padding: 1rem; border-radius: 8px; text-align: center; }
    .feature-icon { font-size: 2rem; margin-bottom: 0.5rem; }
    ul { padding-left: 1.5rem; line-height: 1.8; }
    </style>
</head>
<body>
    <div class="container">
        <div class="flex" style="justify-content: space-between; align-items: center; margin-bottom: 1rem;">
            <div>
                <h1 style="margin-bottom: 0;">Getting Started</h1>
                <span style="color: #555; font-size: 0.85em;">{{.Version}}</span>
            </div>
            <a href="/admin"><button class="secondary">Back to Admin</button></a>
        </div>

        <div class="card">
            <h2>What is Homelab Horizon?</h2>
            <p style="margin-bottom: 1rem;">
                Homelab Horizon helps you manage services in your home lab or site location. It provides:
            </p>
            <div class="feature-grid">
                <div class="feature">
                    <div class="feature-icon">🔐</div>
                    <strong>WireGuard VPN</strong>
                    <p style="color: #888; font-size: 0.9em;">Secure remote access with easy client management</p>
                </div>
                <div class="feature">
                    <div class="feature-icon">🌐</div>
                    <strong>Split-Horizon DNS</strong>
                    <p style="color: #888; font-size: 0.9em;">Internal and external DNS for your services</p>
                </div>
                <div class="feature">
                    <div class="feature-icon">🔄</div>
                    <strong>Reverse Proxy</strong>
                    <p style="color: #888; font-size: 0.9em;">HAProxy with automatic SSL certificates</p>
                </div>
                <div class="feature">
                    <div class="feature-icon">💚</div>
                    <strong>Health Checks</strong>
                    <p style="color: #888; font-size: 0.9em;">Monitor services with ntfy notifications</p>
                </div>
            </div>
        </div>

        <div class="card">
            <h2>Setup Guide</h2>

            <div class="step">
                <span class="step-number">1</span>
                <h3>Get a Domain</h3>
                <div class="step-content">
                    <p>You need a domain where you control DNS. Supported providers:</p>
                    <ul>
                        <li><strong>AWS Route53</strong> - Use an existing AWS account with hosted zones</li>
                        <li><strong>Name.com</strong> - Enable API access in account settings</li>
                    </ul>
                    <div class="tip">
                        <strong>Tip:</strong> A domain like <code>home.example.com</code> lets you create services like
                        <code>grafana.home.example.com</code>, <code>plex.home.example.com</code>, etc.
                    </div>
                </div>
            </div>

            <div class="step">
                <span class="step-number">2</span>
                <h3>Install on an Always-On Device</h3>
                <div class="step-content">
                    <p>Run Homelab Horizon on a device running <strong>Ubuntu/Debian</strong>:</p>
                    <ul>
                        <li>Raspberry Pi with Ubuntu/Raspbian (recommended)</li>
                        <li>Old laptop or mini PC with Ubuntu</li>
                        <li>Any Ubuntu/Debian server</li>
                    </ul>
                    <pre style="margin: 0.5rem 0;"># Install required packages
sudo apt update
sudo apt install wireguard-tools haproxy dnsmasq

# Download and run (requires sudo for WireGuard and ports 80/443)
sudo ./homelab-horizon

# Or install as a systemd service
sudo cp homelab-horizon /usr/local/bin/
# Then use Setup → Install Systemd Service</pre>
                    <div class="warning">
                        <strong>Note:</strong> Must run as root/sudo for WireGuard interface management and binding to ports 80/443.
                    </div>
                </div>
            </div>

            <div class="step">
                <span class="step-number">3</span>
                <h3>Configure Your Router</h3>
                <div class="step-content">
                    <p>Three things to set up on your router:</p>
                    <ul>
                        <li>
                            <strong>Static DHCP Reservation</strong><br>
                            <span style="color: #888;">Give your Homelab Horizon device a fixed IP address (e.g., 192.168.1.10)</span>
                        </li>
                        <li>
                            <strong>DNS Server</strong><br>
                            <span style="color: #888;">Point your network's DNS to the Homelab Horizon device so internal names resolve</span>
                        </li>
                        <li>
                            <strong>Port Forwarding</strong><br>
                            <span style="color: #888;">Forward these ports to the Homelab Horizon device:</span>
                            <ul>
                                <li><code>51820/UDP</code> - WireGuard VPN</li>
                                <li><code>80/TCP</code> - HTTP (for Let's Encrypt challenges)</li>
                                <li><code>443/TCP</code> - HTTPS (for reverse proxy)</li>
                            </ul>
                        </li>
                    </ul>
                    <div class="tip">
                        <strong>Tip:</strong> Some routers call this "Virtual Server" or "NAT Rules" instead of "Port Forwarding".
                        You can verify these settings by running the <strong>Connectivity Tests</strong> on the <a href="/admin/setup" style="color: #2ecc71;">Setup page</a>.
                    </div>
                </div>
            </div>

            <div class="step">
                <span class="step-number">4</span>
                <h3>Configure Your Services</h3>
                <div class="step-content">
                    <p>Now set up your homelab services:</p>
                    <ol style="padding-left: 1.5rem; line-height: 2;">
                        <li><strong>Add a Zone</strong> - Connect your domain with DNS credentials</li>
                        <li><strong>Add Services</strong> - For each service (Grafana, Plex, Home Assistant, etc.):
                            <ul>
                                <li>Set internal DNS (so VPN clients and local network can reach it)</li>
                                <li>Optionally enable external DNS (for public access)</li>
                                <li>Optionally enable HAProxy (for SSL termination and reverse proxy)</li>
                            </ul>
                        </li>
                        <li><strong>Create VPN Clients</strong> - Generate configs for your devices</li>
                        <li><strong>Set Up Health Checks</strong> - Monitor your services and get notified when they go down</li>
                    </ol>
                </div>
            </div>
        </div>

        <div class="card">
            <h2>Example Setup</h2>
            <p style="margin-bottom: 1rem;">Here's a typical homelab configuration:</p>
            <table>
                <thead>
                    <tr><th>Service</th><th>Internal IP</th><th>Domain</th><th>Access</th></tr>
                </thead>
                <tbody>
                    <tr>
                        <td>Grafana</td>
                        <td><code>192.168.1.50:3000</code></td>
                        <td><code>grafana.home.example.com</code></td>
                        <td>VPN + Public (via HAProxy)</td>
                    </tr>
                    <tr>
                        <td>Home Assistant</td>
                        <td><code>192.168.1.51:8123</code></td>
                        <td><code>ha.home.example.com</code></td>
                        <td>VPN + Public (via HAProxy)</td>
                    </tr>
                    <tr>
                        <td>Plex</td>
                        <td><code>192.168.1.52:32400</code></td>
                        <td><code>plex.home.example.com</code></td>
                        <td>VPN only (internal DNS)</td>
                    </tr>
                    <tr>
                        <td>NAS</td>
                        <td><code>192.168.1.20</code></td>
                        <td><code>nas.home.example.com</code></td>
                        <td>VPN only (internal DNS)</td>
                    </tr>
                </tbody>
            </table>
        </div>

        <div class="card">
            <h2>How Split-Horizon DNS Works</h2>
            <p style="margin-bottom: 1rem;">Your services get different IPs depending on where you're connecting from:</p>
            <table>
                <thead>
                    <tr><th>Location</th><th>DNS Resolution</th><th>Path</th></tr>
                </thead>
                <tbody>
                    <tr>
                        <td><strong>On VPN</strong></td>
                        <td>→ Internal IP (e.g., 192.168.1.50)</td>
                        <td>Direct to service</td>
                    </tr>
                    <tr>
                        <td><strong>Local Network</strong></td>
                        <td>→ Internal IP (via dnsmasq)</td>
                        <td>Direct to service</td>
                    </tr>
                    <tr>
                        <td><strong>Public Internet</strong></td>
                        <td>→ Your Public IP</td>
                        <td>Router → HAProxy → Service</td>
                    </tr>
                </tbody>
            </table>
            <div class="tip" style="margin-top: 1rem;">
                <strong>Result:</strong> Same domain name works everywhere - fast local access when home, secure remote access via VPN or HAProxy when away.
            </div>
        </div>

        <div class="card" style="text-align: center; background: linear-gradient(135deg, #0f3460 0%, #16213e 100%);">
            <h2>Ready to Get Started?</h2>
            <p style="margin-bottom: 1rem; color: #ccc;">Let's configure your homelab network.</p>
            <a href="/admin/setup"><button class="success" style="font-size: 1.1em; padding: 0.75rem 2rem;">Continue to Setup</button></a>
        </div>

        <div class="card">
            <h2>Quick Links</h2>
            <div class="flex" style="gap: 0.5rem; flex-wrap: wrap;">
                <a href="/admin"><button>Admin Dashboard</button></a>
                <a href="/admin/setup"><button class="secondary">Setup & Config</button></a>
                <a href="/admin/dns"><button class="secondary">External DNS</button></a>
                <a href="/admin/haproxy"><button class="secondary">HAProxy</button></a>
                <a href="/admin/checks"><button class="secondary">Health Checks</button></a>
            </div>
        </div>

        <div class="card">
            <h2>Resources</h2>
            <p style="margin-bottom: 0.5rem;">
                <a href="https://github.com/IodeSystems/homelab-horizon" target="_blank" style="color: #3498db;">
                    GitHub Repository
                </a> - Source code, issues, and documentation
            </p>
            <p style="color: #888; font-size: 0.9em;">
                Homelab Horizon is open source software licensed under the MIT License.
            </p>
        </div>
    </div>
</body>
</html>`
