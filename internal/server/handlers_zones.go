package server

import (
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

func (s *Server) syncLetsEncrypt() {
	// Derive SSL domains from zones
	s.letsencrypt = letsencrypt.New(letsencrypt.Config{
		Domains:        s.cfg().DeriveSSLDomains(),
		CertDir:        s.cfg().SSLCertDir,
		HAProxyCertDir: s.cfg().SSLHAProxyCertDir,
	})
}
