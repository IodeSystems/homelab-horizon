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

- **One Wildcard Cert**: A single `*.yourdomain.com` certificate covers all your services, inside and out
- **HTTPS Everywhere**: Same SSL cert works internally - no more HTTP fallbacks or certificate warnings on your LAN
- **Automatic DNS Sync**: Add a service, DNS records update automatically (Route53 or Name.com)
- **Split-Horizon Built-in**: Services resolve to internal IPs on your network/VPN, external IPs from the internet
- **Self-Service VPN**: Generate invite links - users scan a QR code and they're connected
- **Unified Dashboard**: See all your services, their health status, DNS records, and SSL certificates in one place

## Screenshots

### Admin Dashboard
![Admin Dashboard](docs/screenshots/admin.png)

### HAProxy & SSL Management
![HAProxy & SSL](docs/screenshots/haproxy.png)

## Features

- **WireGuard VPN Management**: Create clients, generate QR codes, manage peers
- **Split-Horizon DNS**: Internal DNS via dnsmasq, external DNS via Route53 or Name.com
- **Reverse Proxy**: HAProxy with automatic Let's Encrypt wildcard SSL certificates
- **Service Monitoring**: Health checks with ntfy push notifications
- **Self-Service Onboarding**: Users redeem invite tokens to get VPN configs

## Quick Start

### 1. Install Dependencies

```bash
sudo apt update
sudo apt install wireguard-tools haproxy dnsmasq
```

### 2. Build & Run

```bash
# Build
go build -o homelab-horizon .

# Run (requires sudo for WireGuard and ports 80/443)
sudo ./homelab-horizon
```

On first run, an admin token is printed to stdout. Use this to access the web interface.

### 3. Install as Service

```bash
sudo cp homelab-horizon /usr/local/bin/
sudo ./homelab-horizon  # Use Setup -> Install Systemd Service in the web UI
```

## Setup Guide

### Step 1: Get a Domain

You need a domain where you control DNS. Supported providers:

- **AWS Route53**: Install AWS CLI (`sudo snap install aws-cli --classic`) and configure (`aws configure`)
- **Name.com**: Enable API access at name.com/account/settings/security

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
CGO_ENABLED=0 go build -o homelab-horizon .

# Raspberry Pi 4/5 (ARM64)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o homelab-horizon-arm64 .

# Raspberry Pi 2/3 (32-bit ARM)
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -o homelab-horizon-armv7 .
```

Note: `CGO_ENABLED=0` creates a fully static binary with no external dependencies.

## Configuration

Configuration is stored in JSON. Locations searched (in order):

1. `./config.json`
2. `./homelab-horizon.json`
3. `/etc/homelab-horizon/config.json`
4. `/etc/homelab-horizon.json`

### Example Configuration

```json
{
  "listen_addr": ":8080",
  "wg_interface": "wg0",
  "wg_config_path": "/etc/wireguard/wg0.conf",
  "server_endpoint": "vpn.example.com:51820",
  "vpn_range": "10.100.0.0/24",
  "dns": "10.100.0.1",
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
      }
    }
  ],
  "services": [
    {
      "name": "grafana",
      "domain": "grafana.example.com",
      "internal_dns": { "ip": "192.168.1.50" },
      "external_dns": {},
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
| `/admin` | Main dashboard - clients, invites, zones, services |
| `/admin/setup` | System status, requirements, configuration |
| `/admin/dns` | External DNS records and sync status |
| `/admin/haproxy` | Reverse proxy backends and SSL certificates |
| `/admin/checks` | Health check status and notifications |
| `/admin/help` | Getting started guide |

## DNS Providers

### AWS Route53

1. Install: `sudo snap install aws-cli --classic`
2. Configure: `aws configure`
3. Required IAM permissions:
   - `route53:ListHostedZones`
   - `route53:GetHostedZone`
   - `route53:ChangeResourceRecordSets`
   - `route53:GetChange`

### Name.com

1. Enable API access at [name.com/account/settings/security](https://www.name.com/account/settings/security)
2. Create token at [name.com/account/settings/api](https://www.name.com/account/settings/api)
3. Refresh page and copy username + token

Note: Name.com API has rate limits (10 req/sec, 3000/hour). The app handles this automatically.

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
- Go 1.25+ (for building)
- Root access (for WireGuard, ports 80/443, iptables)

Required packages:
- `wireguard-tools` - VPN management
- `haproxy` - Reverse proxy
- `dnsmasq` - Internal DNS

Optional:
- AWS CLI - For Route53 DNS provider

## License

MIT
