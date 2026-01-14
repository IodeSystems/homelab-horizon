package route53

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const awsTimeout = 30 * time.Second

func logAWS(profile, action string) {
	if profile == "" {
		profile = "default"
	}
	fmt.Printf("[AWS/%s] %s\n", profile, action)
}

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

// Available checks if AWS CLI is available
func Available() bool {
	_, err := exec.LookPath("aws")
	return err == nil
}

// GetCurrentValue gets the current value of a DNS record
func GetCurrentValue(zoneID, name, recordType, awsProfile string) (string, error) {
	logAWS(awsProfile, fmt.Sprintf("Getting current value for %s (%s)...", name, recordType))

	ctx, cancel := context.WithTimeout(context.Background(), awsTimeout)
	defer cancel()

	args := []string{
		"route53", "list-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--query", fmt.Sprintf("ResourceRecordSets[?Name=='%s.' && Type=='%s'].ResourceRecords[0].Value", name, recordType),
		"--output", "text",
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	if awsProfile != "" {
		cmd.Env = append(os.Environ(), "AWS_PROFILE="+awsProfile)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("AWS CLI timed out after %v", awsTimeout)
		}
		// Include the AWS error output in the error message
		errMsg := strings.TrimSpace(string(output))
		if errMsg != "" {
			return "", fmt.Errorf("%s: %s", err, errMsg)
		}
		return "", err
	}

	value := strings.TrimSpace(string(output))
	if value == "None" || value == "" {
		logAWS(awsProfile, fmt.Sprintf("Current value for %s: (empty)", name))
		return "", nil
	}
	logAWS(awsProfile, fmt.Sprintf("Current value for %s: %q", name, value))
	return value, nil
}

// UpdateRecord creates or updates a DNS record
func UpdateRecord(r Record) error {
	logAWS(r.AWSProfile, fmt.Sprintf("Updating %s -> %s...", r.Name, r.Value))

	if r.TTL <= 0 {
		r.TTL = 300
	}

	// Ensure name ends with zone name
	if !strings.HasSuffix(r.Name, ".") {
		r.Name = r.Name + "."
	}

	changeBatch := map[string]interface{}{
		"Changes": []map[string]interface{}{
			{
				"Action": "UPSERT",
				"ResourceRecordSet": map[string]interface{}{
					"Name": r.Name,
					"Type": r.Type,
					"TTL":  r.TTL,
					"ResourceRecords": []map[string]string{
						{"Value": r.Value},
					},
				},
			},
		},
	}

	changeBatchJSON, err := json.Marshal(changeBatch)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), awsTimeout)
	defer cancel()

	args := []string{
		"route53", "change-resource-record-sets",
		"--hosted-zone-id", r.ZoneID,
		"--change-batch", string(changeBatchJSON),
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	if r.AWSProfile != "" {
		cmd.Env = append(os.Environ(), "AWS_PROFILE="+r.AWSProfile)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			logAWS(r.AWSProfile, fmt.Sprintf("Update %s TIMEOUT after %v", r.Name, awsTimeout))
			return fmt.Errorf("AWS CLI timed out after %v", awsTimeout)
		}
		logAWS(r.AWSProfile, fmt.Sprintf("Update %s FAILED: %s", r.Name, string(output)))
		return fmt.Errorf("failed to update record: %s", string(output))
	}

	logAWS(r.AWSProfile, fmt.Sprintf("Update %s SUCCESS", r.Name))
	return nil
}

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
			resp.Body.Close()
			ip := strings.TrimSpace(string(buf[:n]))
			if net.ParseIP(ip) != nil {
				return ip, nil
			}
			lastErr = fmt.Errorf("%s: invalid IP response: %q", svc, ip)
		} else {
			resp.Body.Close()
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
	defer resp.Body.Close()

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

// SyncRecord updates a record if the value has changed
func SyncRecord(r Record) (changed bool, err error) {
	currentValue, err := GetCurrentValue(r.ZoneID, r.Name, r.Type, r.AWSProfile)
	if err != nil {
		errStr := err.Error()
		// Check if this is a permission error - don't try to create if we can't even read
		if strings.Contains(errStr, "AccessDenied") || strings.Contains(errStr, "not authorized") {
			logAWS(r.AWSProfile, fmt.Sprintf("Permission denied for %s: %v", r.Name, err))
			return false, err
		}
		logAWS(r.AWSProfile, fmt.Sprintf("GetCurrentValue error for %s: %v, will try to create", r.Name, err))
		// Record might not exist, try to create it
		return true, UpdateRecord(r)
	}

	if currentValue == r.Value {
		logAWS(r.AWSProfile, fmt.Sprintf("%s already set to %s", r.Name, r.Value))
		return false, nil
	}

	logAWS(r.AWSProfile, fmt.Sprintf("%s value mismatch: current=%q new=%q", r.Name, currentValue, r.Value))
	return true, UpdateRecord(r)
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
