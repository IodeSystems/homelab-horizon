package server

import (
	"homelab-horizon/internal/letsencrypt"
)

func (s *Server) syncLetsEncrypt() {
	// Derive SSL domains from zones
	s.letsencrypt = letsencrypt.New(letsencrypt.Config{
		Domains:        s.config.DeriveSSLDomains(),
		CertDir:        s.config.SSLCertDir,
		HAProxyCertDir: s.config.SSLHAProxyCertDir,
	})
}
