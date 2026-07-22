package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// runSetup walks an interactive questionnaire, builds a ServiceRequest, shows
// it, and (on confirm) creates the service and triggers a sync.
func runSetup(c *client, _ []string) error {
	in := bufio.NewReader(os.Stdin)
	fmt.Println("hz setup — create a service")
	fmt.Println()

	name := ask(in, "Service name", "")
	if name == "" {
		return fmt.Errorf("name is required")
	}
	domain := ask(in, "Primary domain (e.g. ebb.iodesystems.com)", "")
	if domain == "" {
		return fmt.Errorf("domain is required")
	}
	domains := []string{domain}
	if extra := ask(in, "Additional domains (comma-separated, optional)", ""); extra != "" {
		for _, d := range strings.Split(extra, ",") {
			if d = strings.TrimSpace(d); d != "" {
				domains = append(domains, d)
			}
		}
	}

	req := apitypes.ServiceRequest{Name: name, Domains: domains}

	kind := ask(in, "Target type: [b]ackend / [s]tatic folder / [n]one", "b")
	switch strings.ToLower(kind) {
	case "b", "backend", "":
		backend := ask(in, "Backend host:port (LAN, e.g. 192.168.1.76:8300)", "")
		if backend == "" {
			return fmt.Errorf("backend is required for a backend service")
		}
		p := &apitypes.ServiceRequestProxy{Backend: backend}
		if hc := ask(in, "Health-check path (optional, e.g. /healthz)", ""); hc != "" {
			p.HealthCheck = &apitypes.ServiceRequestHealthCheck{Path: hc}
		}
		p.InternalOnly = askBool(in, "Internal-only (not public)?", false)
		if next := ask(in, "Blue-green standby backend host:port (optional)", ""); next != "" {
			p.Deploy = &apitypes.ServiceRequestDeploy{NextBackend: next}
			bal := ask(in, "  Balance [first/roundrobin]", "first")
			p.Deploy.Balance = bal
		}
		req.Proxy = p
	case "s", "static":
		root := ask(in, "Absolute static root directory", "")
		if root == "" {
			return fmt.Errorf("static root is required")
		}
		p := &apitypes.ServiceRequestProxy{StaticRoot: root}
		p.SPA = askBool(in, "SPA (serve index.html for unknown paths)?", false)
		p.InternalOnly = askBool(in, "Internal-only (not public)?", false)
		req.Proxy = p
	case "n", "none":
		// DNS-only service.
	default:
		return fmt.Errorf("unknown target type: %s", kind)
	}

	if askBool(in, "Add an internal (dnsmasq) DNS record?", false) {
		ip := ask(in, "  Internal IP", "")
		if ip != "" {
			req.InternalDNS = &apitypes.ServiceRequestInternalDNS{IP: ip}
		}
	}
	if askBool(in, "Add an external (public) DNS record?", false) {
		ips := ask(in, "  External IP(s), comma-separated (blank = host's public IP)", "")
		ext := &apitypes.ServiceRequestExternalDNS{TTL: 300}
		for _, ip := range strings.Split(ips, ",") {
			if ip = strings.TrimSpace(ip); ip != "" {
				ext.IPs = append(ext.IPs, ip)
			}
		}
		if ttlStr := ask(in, "  TTL seconds", "300"); ttlStr != "" {
			if ttl, err := strconv.Atoi(ttlStr); err == nil {
				ext.TTL = ttl
			}
		}
		req.ExternalDNS = ext
	}

	if askBool(in, "Enable Prometheus metrics scraping?", false) {
		m := &apitypes.ServiceRequestMetrics{Enabled: true}
		m.Path = ask(in, "  Metrics path", "/metrics")
		m.Bearer = ask(in, "  Bearer token (optional)", "")
		req.Integrations = &apitypes.ServiceRequestIntegrations{Metrics: m}
	}

	fmt.Println()
	fmt.Println("Request to be sent:")
	b, _ := json.MarshalIndent(req, "  ", "  ")
	fmt.Println("  " + string(b))
	fmt.Println()

	if !askBool(in, "Create this service?", true) {
		fmt.Println("Aborted.")
		return nil
	}
	if err := c.do("POST", "/api/v1/services/add", req, nil); err != nil {
		return err
	}
	fmt.Printf("Created service %q.\n", req.Name)

	if askBool(in, "Trigger a global sync now?", true) {
		return runSync(c, []string{"--wait"})
	}
	fmt.Println("Skipped sync — run 'hz sync' when ready.")
	return nil
}

func ask(in *bufio.Reader, prompt, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", prompt, def)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func askBool(in *bufio.Reader, prompt string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	fmt.Printf("%s [%s]: ", prompt, d)
	line, _ := in.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}
