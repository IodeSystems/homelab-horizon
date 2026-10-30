package route53

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Record represents a DNS record
type Record struct {
	Name       string `json:"name"`
	Type       string `json:"type"` // A, AAAA, CNAME, TXT
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
	ZoneID     string `json:"zone_id"`
	ZoneName   string `json:"zone_name"`
	AWSProfile string `json:"aws_profile"`
}

// DefaultTTL is the TTL hz publishes when a service does not name its own.
//
// 60 rather than the conventional 300 because every record hz publishes points
// at the same dynamic WAN address. When that address moves, this TTL is one
// half of how long clients keep dialling the old one; the other half is how
// long hz takes to notice the change (public_ip_interval). A roaming VPN client
// pays the sum of the two, because reconnecting after a network switch is when
// it re-resolves its endpoint — which is why the delay looks like a roaming
// problem rather than a DNS one.
const DefaultTTL = 60

// GetPublicIP gets the current public IP address
func GetPublicIP() (string, error) {
	// Try multiple services
	services := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var lastErr error

	for _, svc := range services {
		resp, err := client.Get(svc)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", svc, err)
			continue
		}

		if resp.StatusCode == 200 {
			buf := make([]byte, 64)
			n, _ := resp.Body.Read(buf)
			_ = resp.Body.Close()
			ip := strings.TrimSpace(string(buf[:n]))
			if net.ParseIP(ip) != nil {
				return ip, nil
			}
			lastErr = fmt.Errorf("%s: invalid IP response: %q", svc, ip)
		} else {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("%s: status %d", svc, resp.StatusCode)
		}
	}

	if lastErr != nil {
		return "", fmt.Errorf("could not determine public IP: %w", lastErr)
	}
	return "", fmt.Errorf("could not determine public IP: all services failed")
}

type IPv6Status struct {
	HasAddress     bool
	HasGlobalIP    bool
	Connectivity   bool
	PublicIPv6     string
	Error          string
	Recommendation string
}

func CheckIPv6() IPv6Status {
	status := IPv6Status{}

	ifaces, err := net.Interfaces()
	if err != nil {
		status.Error = err.Error()
		return status
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.To4() != nil {
				continue
			}
			status.HasAddress = true
			if ip.IsGlobalUnicast() && !ip.IsPrivate() {
				status.HasGlobalIP = true
			}
		}
	}

	if !status.HasAddress {
		status.Recommendation = "No IPv6 addresses found. IPv6 may be disabled or not available from your ISP."
		return status
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				d := net.Dialer{}
				return d.DialContext(ctx, "tcp6", addr)
			},
		},
	}

	resp, err := client.Get("https://api6.ipify.org")
	if err != nil {
		status.Error = "IPv6 connectivity test failed: " + err.Error()
		if status.HasGlobalIP {
			status.Recommendation = "You have IPv6 addresses but cannot reach IPv6 internet. Your ISP may be blocking IPv6 or your router needs configuration."
		} else {
			status.Recommendation = "Only link-local IPv6 addresses found. Your ISP may not provide IPv6."
		}
		return status
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == 200 {
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		ip := strings.TrimSpace(string(buf[:n]))
		if net.ParseIP(ip) != nil {
			status.Connectivity = true
			status.PublicIPv6 = ip
		}
	}

	return status
}

// VerifyPropagation checks if a DNS record has propagated by querying public DNS servers
// Returns true if the record resolves to the expected value, false otherwise
// For wildcard domains (*.example.com), queries _acme-challenge subdomain since that's what Let's Encrypt uses
func VerifyPropagation(name, expectedValue string, timeout time.Duration) bool {
	// Handle wildcard domains by querying the _acme-challenge subdomain
	// This is what Let's Encrypt will use for DNS-01 challenge verification
	queryName := name
	if strings.HasPrefix(name, "*.") {
		// Convert *.dev.example.com to _acme-challenge.dev.example.com
		queryName = "_acme-challenge" + name[1:]
	}

	// Public DNS servers to check
	dnsServers := []string{
		"8.8.8.8:53",        // Google
		"1.1.1.1:53",        // Cloudflare
		"208.67.222.222:53", // OpenDNS
	}

	deadline := time.Now().Add(timeout)
	checkInterval := 5 * time.Second

	for time.Now().Before(deadline) {
		allMatch := true
		for _, server := range dnsServers {
			resolver := &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					d := net.Dialer{Timeout: 5 * time.Second}
					return d.DialContext(ctx, "udp", server)
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			ips, err := resolver.LookupHost(ctx, queryName)
			cancel()

			if err != nil {
				allMatch = false
				break
			}

			found := false
			for _, ip := range ips {
				if ip == expectedValue {
					found = true
					break
				}
			}
			if !found {
				allMatch = false
				break
			}
		}

		if allMatch {
			return true
		}

		time.Sleep(checkInterval)
	}

	return false
}

// GenerateIAMPolicy generates a fine-grained IAM policy for the given zone IDs
func GenerateIAMPolicy(zoneIDs []string) string {
	// Build zone ARNs
	var zoneARNs []string
	for _, zoneID := range zoneIDs {
		// Remove /hostedzone/ prefix if present
		zoneID = strings.TrimPrefix(zoneID, "/hostedzone/")
		zoneARNs = append(zoneARNs, fmt.Sprintf("arn:aws:route53:::hostedzone/%s", zoneID))
	}

	// If no specific zones, use wildcard
	if len(zoneARNs) == 0 {
		zoneARNs = []string{"arn:aws:route53:::hostedzone/*"}
	}

	policy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Sid":    "Route53ListZones",
				"Effect": "Allow",
				"Action": []string{
					"route53:ListHostedZones",
					"route53:ListHostedZonesByName",
				},
				"Resource": "*",
			},
			{
				"Sid":    "Route53ManageRecords",
				"Effect": "Allow",
				"Action": []string{
					"route53:GetHostedZone",
					"route53:ListResourceRecordSets",
					"route53:ChangeResourceRecordSets",
				},
				"Resource": zoneARNs,
			},
			{
				"Sid":    "Route53GetChanges",
				"Effect": "Allow",
				"Action": []string{
					"route53:GetChange",
				},
				"Resource": "arn:aws:route53:::change/*",
			},
		},
	}

	data, _ := json.MarshalIndent(policy, "", "  ")
	return string(data)
}
