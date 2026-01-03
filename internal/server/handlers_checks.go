package server

import (
	"net/http"
	"strconv"
	"strings"

	"homelab-horizon/internal/config"
)

// Service Monitoring Handlers

func (s *Server) handleChecks(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	statuses := s.monitor.GetStatuses()

	data := map[string]interface{}{
		"Config":    s.config,
		"Statuses":  statuses,
		"Message":   r.URL.Query().Get("msg"),
		"Error":     r.URL.Query().Get("err"),
		"CSRFToken": s.getCSRFToken(r),
	}
	s.templates["checks"].Execute(w, data)
}

func (s *Server) handleAddCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	checkType := strings.TrimSpace(r.FormValue("type"))
	target := strings.TrimSpace(r.FormValue("target"))
	intervalStr := r.FormValue("interval")

	if name == "" || checkType == "" || target == "" {
		http.Redirect(w, r, "/admin/checks?err=Name,+type,+and+target+required", http.StatusSeeOther)
		return
	}

	// Check for duplicate
	for _, c := range s.config.ServiceChecks {
		if c.Name == name {
			http.Redirect(w, r, "/admin/checks?err=Check+with+this+name+already+exists", http.StatusSeeOther)
			return
		}
	}

	interval := 300
	if intervalStr != "" {
		if i, err := strconv.Atoi(intervalStr); err == nil && i > 0 {
			interval = i
		}
	}

	check := config.ServiceCheck{
		Name:     name,
		Type:     checkType,
		Target:   target,
		Interval: interval,
		Enabled:  true,
	}

	s.config.ServiceChecks = append(s.config.ServiceChecks, check)
	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin/checks?err="+err.Error(), http.StatusSeeOther)
		return
	}

	// Reload monitor to pick up new check
	s.monitor.Reload(s.config)

	http.Redirect(w, r, "/admin/checks?msg=Check+added", http.StatusSeeOther)
}

func (s *Server) handleDeleteCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin/checks?err=Name+required", http.StatusSeeOther)
		return
	}

	// Find and remove the check
	found := false
	for i, c := range s.config.ServiceChecks {
		if c.Name == name {
			s.config.ServiceChecks = append(s.config.ServiceChecks[:i], s.config.ServiceChecks[i+1:]...)
			found = true
			break
		}
	}

	if !found {
		http.Redirect(w, r, "/admin/checks?err=Check+not+found", http.StatusSeeOther)
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin/checks?err="+err.Error(), http.StatusSeeOther)
		return
	}

	// Reload monitor
	s.monitor.Reload(s.config)

	http.Redirect(w, r, "/admin/checks?msg=Check+deleted", http.StatusSeeOther)
}

func (s *Server) handleToggleCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin/checks?err=Name+required", http.StatusSeeOther)
		return
	}

	// Toggle in monitor
	status := s.monitor.GetStatus(name)
	if status == nil {
		http.Redirect(w, r, "/admin/checks?err=Check+not+found", http.StatusSeeOther)
		return
	}

	newEnabled := !status.Enabled
	s.monitor.SetCheckEnabled(name, newEnabled)

	// Update config
	if status.AutoGen {
		// For auto-generated checks, update the disabled list
		s.monitor.UpdateConfig()
	} else {
		// For manual checks, update the check directly
		for i := range s.config.ServiceChecks {
			if s.config.ServiceChecks[i].Name == name {
				s.config.ServiceChecks[i].Enabled = newEnabled
				break
			}
		}
	}
	config.Save(s.configPath, s.config)

	state := "enabled"
	if !newEnabled {
		state = "disabled"
	}
	http.Redirect(w, r, "/admin/checks?msg=Check+"+state, http.StatusSeeOther)
}

func (s *Server) handleRunCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin/checks?err=Name+required", http.StatusSeeOther)
		return
	}

	status := s.monitor.RunCheck(name)
	if status == nil {
		http.Redirect(w, r, "/admin/checks?err=Check+not+found", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/checks?msg=Check+executed:+"+status.Status, http.StatusSeeOther)
}

func (s *Server) handleCheckSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	ntfyURL := strings.TrimSpace(r.FormValue("ntfy_url"))
	s.config.NtfyURL = ntfyURL

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin/checks?err="+err.Error(), http.StatusSeeOther)
		return
	}

	// Reload monitor with new config
	s.monitor.Reload(s.config)

	http.Redirect(w, r, "/admin/checks?msg=Settings+saved", http.StatusSeeOther)
}
