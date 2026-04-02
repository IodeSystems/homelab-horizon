package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/dnsmasq"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// MCPServer wraps the homelab-horizon Server and exposes it as an MCP tool server.
type MCPServer struct {
	srv *Server
	mcp *mcpserver.MCPServer
}

// NewMCPServer creates an MCP server backed by the given homelab-horizon Server.
func NewMCPServer(srv *Server, version string) *MCPServer {
	m := &MCPServer{srv: srv}
	m.mcp = mcpserver.NewMCPServer(
		"homelab-horizon",
		version,
		mcpserver.WithToolCapabilities(false),
	)
	m.registerTools()
	return m
}

// ServeStdio runs the MCP server over stdin/stdout.
func (m *MCPServer) ServeStdio() error {
	return mcpserver.ServeStdio(m.mcp)
}

// StreamableHTTPHandler returns an http.Handler that serves MCP over Streamable HTTP.
func (m *MCPServer) StreamableHTTPHandler() http.Handler {
	return mcpserver.NewStreamableHTTPServer(m.mcp)
}

func (m *MCPServer) registerTools() {
	m.mcp.AddTool(
		mcp.NewTool("get_topology",
			mcp.WithDescription("Get the full homelab topology: zones, services, DNS mappings, and HAProxy backends"),
		),
		m.handleGetTopology,
	)

	m.mcp.AddTool(
		mcp.NewTool("get_status",
			mcp.WithDescription("Get system status: WireGuard interface/peers, dnsmasq, HAProxy, and service health checks"),
		),
		m.handleGetStatus,
	)

	m.mcp.AddTool(
		mcp.NewTool("update_service",
			mcp.WithDescription("Add, edit, or delete a service. After mutation, subsystems (dnsmasq, HAProxy) are resynced automatically."),
			mcp.WithString("action", mcp.Required(),
				mcp.Description("Action to perform"),
				mcp.Enum("add", "edit", "delete"),
			),
			mcp.WithString("name", mcp.Required(),
				mcp.Description("Service name"),
			),
			mcp.WithString("domain",
				mcp.Description("FQDN for the service (required for add/edit)"),
			),
			mcp.WithString("internal_ip",
				mcp.Description("Internal DNS IP for VPN clients (dnsmasq)"),
			),
			mcp.WithBoolean("external_enabled",
				mcp.Description("Enable external DNS for this service"),
			),
			mcp.WithString("external_ip",
				mcp.Description("External DNS IP (empty = auto-detect public IP)"),
			),
			mcp.WithNumber("ttl",
				mcp.Description("DNS TTL in seconds (default 300)"),
			),
			mcp.WithBoolean("proxy_enabled",
				mcp.Description("Enable HAProxy reverse proxy"),
			),
			mcp.WithString("proxy_backend",
				mcp.Description("HAProxy backend address (host:port)"),
			),
			mcp.WithString("health_check_path",
				mcp.Description("HTTP health check path for HAProxy (e.g. /health)"),
			),
			mcp.WithBoolean("internal_only",
				mcp.Description("Restrict HAProxy to local network only"),
			),
		),
		m.handleUpdateService,
	)

	m.mcp.AddTool(
		mcp.NewTool("get_host_port_map",
			mcp.WithDescription("Get a map of all hosts and their reserved ports, derived from service backends, HAProxy, WireGuard, dnsmasq, and the admin server"),
		),
		m.handleGetHostPortMap,
	)

	m.mcp.AddTool(
		mcp.NewTool("sync",
			mcp.WithDescription("Trigger a full sync of all subsystems: dnsmasq, external DNS, SSL certificates, and HAProxy. Returns the sync log."),
		),
		m.handleSync,
	)
}

func (m *MCPServer) handleGetTopology(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cfg := m.srv.config

	services := make([]config.Service, len(cfg.Services))
	copy(services, cfg.Services)
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })

	dnsMappings := cfg.DeriveDNSMappings()
	haproxyBackends := cfg.DeriveHAProxyBackends()

	var serviceDomains []string
	for _, svc := range cfg.Services {
		serviceDomains = append(serviceDomains, svc.Domains...)
	}
	var publicDNS, privateDNS map[string]string
	if len(cfg.UpstreamDNS) > 0 && len(serviceDomains) > 0 {
		publicDNS = dnsmasq.ResolveAllWith(serviceDomains, cfg.UpstreamDNS[0])
	}
	if len(serviceDomains) > 0 && cfg.DNS != "" {
		privateDNS = dnsmasq.ResolveAllWith(serviceDomains, cfg.DNS)
	}

	result := map[string]any{
		"zones":            cfg.Zones,
		"services":         services,
		"dns_mappings":     dnsMappings,
		"haproxy_backends": haproxyBackends,
		"public_dns":       publicDNS,
		"private_dns":      privateDNS,
		"public_ip":        cfg.PublicIP,
		"vpn_range":        cfg.VPNRange,
		"wg_gateway_ip":    cfg.GetWGGatewayIP(),
	}

	return jsonResult(result)
}

func (m *MCPServer) handleGetStatus(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m.srv.wg.Load()
	ifaceStatus := m.srv.wg.GetInterfaceStatus()
	peers := m.srv.wg.GetPeers()

	type peerInfo struct {
		Name            string `json:"name"`
		PublicKey       string `json:"public_key"`
		AllowedIPs      string `json:"allowed_ips"`
		Endpoint        string `json:"endpoint,omitempty"`
		LatestHandshake string `json:"latest_handshake,omitempty"`
		TransferRx      string `json:"transfer_rx,omitempty"`
		TransferTx      string `json:"transfer_tx,omitempty"`
		Online          bool   `json:"online"`
		IsAdmin         bool   `json:"is_admin"`
	}

	var peerData []peerInfo
	for _, p := range peers {
		pi := peerInfo{
			Name:       p.Name,
			PublicKey:  p.PublicKey,
			AllowedIPs: p.AllowedIPs,
		}
		if status, ok := ifaceStatus.Peers[p.PublicKey]; ok {
			pi.Endpoint = status.Endpoint
			pi.LatestHandshake = status.LatestHandshake
			pi.TransferRx = status.TransferRx
			pi.TransferTx = status.TransferTx
			pi.Online = status.LatestHandshake != ""
		}
		for _, adminName := range m.srv.config.VPNAdmins {
			if p.Name == adminName {
				pi.IsAdmin = true
				break
			}
		}
		peerData = append(peerData, pi)
	}

	dnsStatus := m.srv.dns.Status()
	hapStatus := m.srv.haproxy.GetStatus()
	checkStatuses := m.srv.monitor.GetStatuses()

	result := map[string]any{
		"wireguard": map[string]any{
			"interface_up": ifaceStatus.Up,
			"public_key":   ifaceStatus.PublicKey,
			"port":         ifaceStatus.Port,
			"peers":        peerData,
		},
		"dnsmasq": map[string]any{
			"enabled":       m.srv.config.DNSMasqEnabled,
			"running":       dnsStatus.Running,
			"config_exists": dnsStatus.ConfigExists,
			"error":         dnsStatus.Error,
		},
		"haproxy": map[string]any{
			"enabled":       m.srv.config.HAProxyEnabled,
			"running":       hapStatus.Running,
			"config_exists": hapStatus.ConfigExists,
			"version":       hapStatus.Version,
			"error":         hapStatus.Error,
		},
		"ssl_enabled":    m.srv.config.SSLEnabled,
		"health_checks":  checkStatuses,
		"overall_health": m.srv.health.IsHealthy(),
	}

	return jsonResult(result)
}

func (m *MCPServer) handleUpdateService(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	action, err := req.RequireString("action")
	if err != nil {
		return mcp.NewToolResultError("action is required: add, edit, or delete"), nil
	}
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError("name is required"), nil
	}

	switch action {
	case "delete":
		if !m.srv.config.RemoveService(name) {
			return mcp.NewToolResultError(fmt.Sprintf("service %q not found", name)), nil
		}
		if err := config.Save(m.srv.configPath, m.srv.config); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("save failed: %s", err)), nil
		}
		m.srv.syncServices()
		return mcp.NewToolResultText(fmt.Sprintf("Service %q deleted", name)), nil

	case "add":
		domain, err := req.RequireString("domain")
		if err != nil {
			return mcp.NewToolResultError("domain is required for add"), nil
		}
		svc := m.buildService(name, domain, req)
		if err := m.srv.config.AddService(svc); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := config.Save(m.srv.configPath, m.srv.config); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("save failed: %s", err)), nil
		}
		m.srv.syncServices()
		return mcp.NewToolResultText(fmt.Sprintf("Service %q added", name)), nil

	case "edit":
		domain := req.GetString("domain", "")
		existing := m.srv.config.GetService(name)
		if existing == nil {
			return mcp.NewToolResultError(fmt.Sprintf("service %q not found", name)), nil
		}
		if domain == "" {
			domain = strings.Join(existing.Domains, ",")
		}

		m.srv.config.RemoveService(name)
		svc := m.buildService(name, domain, req)

		// Preserve existing config for fields not provided
		args := req.GetArguments()
		if svc.InternalDNS == nil && existing.InternalDNS != nil {
			if _, ok := args["internal_ip"]; !ok {
				svc.InternalDNS = existing.InternalDNS
			}
		}
		if svc.ExternalDNS == nil && existing.ExternalDNS != nil {
			if _, ok := args["external_enabled"]; !ok {
				svc.ExternalDNS = existing.ExternalDNS
			}
		}
		if svc.Proxy == nil && existing.Proxy != nil {
			if _, ok := args["proxy_enabled"]; !ok {
				svc.Proxy = existing.Proxy
			}
		}

		if err := m.srv.config.AddService(svc); err != nil {
			m.srv.config.AddService(*existing)
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := config.Save(m.srv.configPath, m.srv.config); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("save failed: %s", err)), nil
		}
		m.srv.syncServices()
		return mcp.NewToolResultText(fmt.Sprintf("Service %q updated", name)), nil

	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown action %q (use add, edit, or delete)", action)), nil
	}
}

func (m *MCPServer) buildService(name, domain string, req mcp.CallToolRequest) config.Service {
	var domains []string
	for _, d := range strings.Split(domain, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			domains = append(domains, d)
		}
	}
	svc := config.Service{
		Name:    name,
		Domains: domains,
	}

	if ip := req.GetString("internal_ip", ""); ip != "" {
		svc.InternalDNS = &config.InternalDNS{IP: ip}
	}

	if req.GetBool("external_enabled", false) {
		ttl := req.GetInt("ttl", 300)
		svc.ExternalDNS = &config.ExternalDNS{
			IP:  req.GetString("external_ip", ""),
			TTL: ttl,
		}
	}

	if req.GetBool("proxy_enabled", false) {
		backend := req.GetString("proxy_backend", "")
		if backend != "" {
			svc.Proxy = &config.ProxyConfig{
				Backend:      backend,
				InternalOnly: req.GetBool("internal_only", false),
			}
			if checkPath := req.GetString("health_check_path", ""); checkPath != "" {
				svc.Proxy.HealthCheck = &config.HealthCheck{Path: checkPath}
			}
		}
	}

	return svc
}

func (m *MCPServer) handleGetHostPortMap(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return jsonResult(m.srv.config.DeriveHostPortMap())
}

func (m *MCPServer) handleSync(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if m.srv.sync.IsRunning() {
		return mcp.NewToolResultError("a sync is already in progress"), nil
	}

	collector := &syncLogCollector{}
	m.srv.runSyncInternal(collector, nil)

	return mcp.NewToolResultText(strings.Join(collector.lines, "\n")), nil
}

// syncLogCollector collects sync log messages as text lines.
type syncLogCollector struct {
	lines []string
}

func (c *syncLogCollector) Info(msg string)    { c.lines = append(c.lines, "[INFO] "+msg) }
func (c *syncLogCollector) Success(msg string) { c.lines = append(c.lines, "[OK] "+msg) }
func (c *syncLogCollector) Warning(msg string) { c.lines = append(c.lines, "[WARN] "+msg) }
func (c *syncLogCollector) Error(msg string)   { c.lines = append(c.lines, "[ERROR] "+msg) }
func (c *syncLogCollector) Step(msg string)    { c.lines = append(c.lines, "\n== "+msg+" ==") }
func (c *syncLogCollector) Done(success bool) {
	if success {
		c.lines = append(c.lines, "\n[DONE] Sync completed successfully")
	} else {
		c.lines = append(c.lines, "\n[DONE] Sync completed with errors")
	}
}

// jsonResult marshals v to JSON and returns it as a tool result.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling result: %w", err)
	}
	return mcp.NewToolResultText(string(data)), nil
}

// GetAdminToken returns the server's admin token for MCP authentication.
func (s *Server) GetAdminToken() string {
	return s.adminToken
}
