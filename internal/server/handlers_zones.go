package server

import (
	"homelab-horizon/internal/letsencrypt"
)

func (s *Server) syncLetsEncrypt() {
	// Derive SSL domains from zones
	s.letsencrypt = letsencrypt.New(letsencrypt.Config{
		Domains:        s.cfg().DeriveSSLDomains(),
		CertDir:        s.cfg().SSLCertDir,
		HAProxyCertDir: s.cfg().SSLHAProxyCertDir,
	})
}
