# Homelab Horizon

A self-contained homelab management tool for WireGuard VPN, split-horizon DNS, reverse proxy, and service monitoring. Single binary, runs on Ubuntu/Debian.

## The Problem

Running a homelab with external access means juggling multiple systems that don't talk to each other:

- **SSL Certificate Sprawl**: Managing 12+ individual certificates, each with their own renewal schedule
- **Internal SSL Headaches**: HTTPS doesn't work from inside your network because certs are tied to external IPs, so you're stuck with HTTP internally or browser warnings
- **Unnecessary Public Exposure**: OAuth callbacks and other endpoints need valid SSL, forcing you to expose internal-only services to the internet just to get certificates
- **Manual DNS Management**: Updating Route53 or other DNS providers by hand every time your IP changes or you add a service
- **Broken Internal Resolution**: Your domains work from the internet but timeout when you're on your own network (the classic split-horizon DNS problem)
- **WireGuard Friction**: Every new device needs a config file, QR code, and manual peer setup on the server
- **Scattered Configuration**: HAProxy configs, DNS records, WireGuard peers, and SSL certs all managed separately with no unified view

## The Solution

Homelab Horizon consolidates all of this into a single web UI:

- **Consolidated Certs**: Wildcard certs only cover one level (`*.example.com` won't cover `app.sub.example.com`), so we make it easy to add extra SANs like `*.vpn.example.com` - all visible and editable in the UI, and you can inspect exactly what each cert covers. No more mystery broken SSL.
- **HTTPS Everywhere**: Same SSL cert works internally - no more HTTP fallbacks or certificate warnings on your LAN
- **Automatic DNS Sync**: Add a service, DNS records update automatically (Route53, Cloudflare, Name.com, and more)
- **Split-Horizon Built-in**: Services resolve to internal IPs on your network/VPN, external IPs from the internet
- **Self-Service VPN**: Generate invite links - users scan a QR code and they're connected
- **Unified Dashboard**: See all your services, their health status, DNS records, and SSL certificates in one place

## Screenshots

### Services
![Services](docs/screenshots/services.png)

### Service Detail
![Service Detail](docs/screenshots/services-detail.png)

### Settings
![Settings](docs/screenshots/settings.png)

## Features

- **Auto-Heal**: Detects and installs missing dependencies on a fresh Ubuntu system
- **WireGuard VPN Management**: Create clients, generate QR codes, manage peers
- **Split-Horizon DNS**: Internal DNS via dnsmasq, external DNS via Route53, Name.com, Cloudflare, and more
- **Reverse Proxy**: HAProxy with automatic Let's Encrypt wildcard SSL certificates
- **Service Monitoring**: Health checks with ntfy push notifications
- **Self-Service Onboarding**: Users redeem invite tokens to get VPN configs
- **IP Banning**: Per-service IP bans with timeout support
- **Rolling Deploys**: Blue-green deployment support with hz-client CLI

## Quick Start

### Option A: Docker Demo

Try it instantly with no setup — auto-installs all dependencies on a vanilla Ubuntu container:

```bash
make docker-run
# or manually:
docker run --rm -p 8080:8080 homelab-horizon:demo
```

Open `http://localhost:8080` and log in with the admin token printed in the container logs.

### Option B: Direct Install

```bash
# Build (requires Go 1.25+ and Node.js)
make

# Run (requires sudo for WireGuard and ports 80/443)
sudo ./homelab-horizon
```

On first run, the binary:
1. Copies itself to `/usr/local/bin/`
2. Installs a systemd service
3. Prints an admin token to stdout

If `auto_heal` is enabled in the config, it will also detect and install missing packages (`wireguard-tools`, `haproxy`, `dnsmasq`) automatically via `apt-get`.

### Environment Variable Config

Pass the full config as JSON via the `HZ_CONFIG` environment variable — useful for Docker and backup/restore:

```bash
sudo HZ_CONFIG='{"listen_addr":":8080","auto_heal":true,...}' ./homelab-horizon
```

## Setup Guide

### Step 1: Get a Domain

You need a domain where you control DNS. Supported providers:

- **AWS Route53**
- **Cloudflare**
- **Name.com**
- **DigitalOcean**
- **Hetzner**
- **Gandi**
- **Google Cloud DNS**
- **DuckDNS**

### Step 2: Configure Your Router

1. **Static DHCP**: Give the Homelab Horizon device a fixed IP
2. **DNS Server**: Point network DNS to the Homelab Horizon device
3. **Port Forwarding**:
   - `51820/UDP` - WireGuard VPN
   - `80/TCP` - HTTP (Let's Encrypt challenges)
   - `443/TCP` - HTTPS (reverse proxy)

### Step 3: Configure Services

1. Add a DNS zone with your domain and provider credentials
2. Add services with internal DNS (for VPN/local access) and optional external DNS
3. Enable HAProxy for services needing SSL termination
4. Create VPN clients for remote access
5. Set up health checks for monitoring

## Architecture

```
Internet                        Your Network
    |                               |
    v                               v
[Router] -----> [Homelab Horizon] <---- [VPN Clients]
    |               |    |    |
    |               |    |    +-- dnsmasq (internal DNS)
    |               |    +------- HAProxy (reverse proxy + SSL)
    |               +------------ WireGuard (VPN server)
    |
    +-- Port 51820 (WireGuard)
    +-- Port 80/443 (HAProxy)
```

### Split-Horizon DNS

| Location | DNS Resolution | Path |
|----------|---------------|------|
| On VPN | Internal IP (e.g., 192.168.1.50) | Direct to service |
| Local Network | Internal IP (via dnsmasq) | Direct to service |
| Public Internet | Your Public IP | Router -> HAProxy -> Service |

## Building

### Quick Build (current platform)

```bash
make
```

### Cross-Platform Builds

Build for all supported platforms:

```bash
make build-all
```

Or build for specific targets:

```bash
make build-linux-amd64   # Linux x86_64 (most servers/VMs)
make build-linux-arm64   # Raspberry Pi 4/5, modern ARM64 servers
make build-linux-arm     # Raspberry Pi 2/3, older 32-bit ARM
```

Binaries are output to `dist/`.

### Create Release Archives

```bash
make release
```

Creates `.tar.gz` archives for each platform in `dist/`.

### Manual Build (without Make)

```bash
# Current system
CGO_ENABLED=0 go build -o homelab-horizon ./cmd/homelab-horizon

# Raspberry Pi 4/5 (ARM64)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o homelab-horizon-arm64 ./cmd/homelab-horizon

# Raspberry Pi 2/3 (32-bit ARM)
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o homelab-horizon-armv7 ./cmd/homelab-horizon
```

Note: `CGO_ENABLED=0` creates a fully static binary with no external dependencies.

## Configuration

Configuration is stored in JSON (with `//` comment support). Locations searched (in order):

1. `/etc/homelab-horizon/config.json`
2. `/etc/homelab-horizon.json`
3. `./config.json`
4. `./homelab-horizon.json`

Alternatively, pass the full config as JSON via the `HZ_CONFIG` environment variable.

### Example Configuration

```json
{
  "listen_addr": ":8080",
  "auto_heal": true,

  "wg_interface": "wg0",
  "wg_config_path": "/etc/wireguard/wg0.conf",
  "server_endpoint": "vpn.example.com:51820",
  "vpn_range": "10.100.0.0/24",
  "dns": "10.100.0.1",

  "dnsmasq_enabled": true,
  "haproxy_enabled": true,
  "ssl_enabled": true,

  "zones": [
    {
      "name": "example.com",
      "zone_id": "Z1234567890",
      "dns_provider": {
        "type": "route53",
        "aws_profile": "default"
      },
      "ssl": {
        "enabled": true,
        "email": "admin@example.com"
      },
      "sub_zones": ["vpn"]
    }
  ],
  "services": [
    {
      "name": "grafana",
      "domains": ["grafana.example.com"],
      "internal_dns": { "ip": "192.168.1.50" },
      "external_dns": { "ttl": 300 },
      "proxy": {
        "backend": "192.168.1.50:3000",
        "health_check": { "path": "/api/health" }
      }
    }
  ],
  "ntfy_url": "https://ntfy.sh/my-homelab-alerts"
}
```

## Web Interface

| Page | Description |
|------|-------------|
| `/app/dashboard` | Overview dashboard |
| `/app/services` | Service management — domains, DNS, proxy, health status |
| `/app/domains` | External DNS records and sync status |
| `/app/vpn` | VPN client management — create clients, QR codes, invites |
| `/app/bans` | IP ban management |
| `/app/checks` | Health check status and notifications |
| `/app/settings` | Zones, HAProxy, SSL, health checks, system config |

## DNS Providers

Configure your provider in the zone's `dns_provider` block:

| Provider | Type | Required Fields |
|----------|------|----------------|
| AWS Route53 | `route53` | `aws_profile` or `aws_access_key_id` + `aws_secret_access_key` |
| Cloudflare | `cloudflare` | `cloudflare_api_token` |
| Name.com | `namecom` | `namecom_username` + `namecom_api_token` |
| DigitalOcean | `digitalocean` | `api_token` |
| Hetzner | `hetzner` | `api_token` |
| Gandi | `gandi` | `api_token` |
| Google Cloud DNS | `googlecloud` | `gcp_project` (+ optional `gcp_service_account_json`) |
| DuckDNS | `duckdns` | `api_token` |

## Health Checks

Services with HAProxy backends automatically get health checks. Configure ntfy URL to receive push notifications when services go down.

Check types:
- **ping**: TCP connect to common ports (80, 443, 22)
- **http**: HTTP GET expecting 200 response

## SSL Certificates

Wildcard certificates are automatically obtained via Let's Encrypt using DNS-01 challenges. Certificates are renewed automatically when within 30 days of expiry.

Certificates cover:
- `*.example.com` (base zone)
- `*.vpn.example.com` (sub-zones you configure)

## Requirements

- Ubuntu/Debian Linux
- Go 1.25+ and Node.js (for building)
- Root access (for WireGuard, ports 80/443, iptables)

Runtime packages (auto-installed when `auto_heal` is enabled):
- `wireguard-tools` - VPN management
- `haproxy` - Reverse proxy (when `haproxy_enabled`)
- `dnsmasq` - Internal DNS (when `dnsmasq_enabled`)
- `iptables` - NAT masquerading
- `qrencode` - VPN client QR codes

## License

MIT
