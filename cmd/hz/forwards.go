package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// parseForwardSpec parses a --forward value, "proto:port:backend-ip:backend-port"
// (e.g. udp:4433:192.168.1.76:4433). Only the shape is checked here; the
// server decides whether the port and backend are allowed.
func parseForwardSpec(s string) (apitypes.ServiceForward, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 4 {
		return apitypes.ServiceForward{}, fmt.Errorf("--forward %q: want proto:port:backend-ip:backend-port, e.g. udp:4433:192.168.1.76:4433", s)
	}
	proto, port, err := parseForwardKey(parts[0] + ":" + parts[1])
	if err != nil {
		return apitypes.ServiceForward{}, fmt.Errorf("--forward %q: %w", s, err)
	}
	ip := net.ParseIP(parts[2])
	if ip == nil || ip.To4() == nil {
		return apitypes.ServiceForward{}, fmt.Errorf("--forward %q: backend %q is not an IPv4 address", s, parts[2])
	}
	bport, err := strconv.Atoi(parts[3])
	if err != nil || bport < 1 || bport > 65535 {
		return apitypes.ServiceForward{}, fmt.Errorf("--forward %q: backend port %q must be 1-65535", s, parts[3])
	}
	return apitypes.ServiceForward{
		Proto:   proto,
		Port:    port,
		Backend: net.JoinHostPort(ip.To4().String(), strconv.Itoa(bport)),
	}, nil
}

// parseForwardKey parses "proto:port", the identity of a forward.
func parseForwardKey(s string) (string, int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("want proto:port, e.g. udp:4433")
	}
	proto := strings.ToLower(parts[0])
	if proto != "udp" && proto != "tcp" {
		return "", 0, fmt.Errorf("proto %q must be udp or tcp", parts[0])
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port %q must be 1-65535", parts[1])
	}
	return proto, port, nil
}

// applyForwardFlags returns existing with each --remove-forward applied, then
// each --forward added. A --forward whose proto:port already exists replaces
// that entry's backend and keeps its name and description. Removing a forward
// the service does not have is an error, so a typo is not a silent no-op.
func applyForwardFlags(existing []apitypes.ServiceForward, add, remove []string) ([]apitypes.ServiceForward, error) {
	out := append([]apitypes.ServiceForward(nil), existing...)

	for _, r := range remove {
		proto, port, err := parseForwardKey(r)
		if err != nil {
			return nil, fmt.Errorf("--remove-forward %q: %w", r, err)
		}
		idx := findForward(out, proto, port)
		if idx < 0 {
			return nil, fmt.Errorf("--remove-forward %q: this service has no %s/%d forward", r, proto, port)
		}
		out = append(out[:idx], out[idx+1:]...)
	}

	for _, a := range add {
		f, err := parseForwardSpec(a)
		if err != nil {
			return nil, err
		}
		if idx := findForward(out, f.Proto, f.Port); idx >= 0 {
			out[idx].Backend = f.Backend
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

func findForward(list []apitypes.ServiceForward, proto string, port int) int {
	for i, f := range list {
		if f.Proto == proto && f.Port == port {
			return i
		}
	}
	return -1
}
