package nested

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

// The I/O half of the certificate pull: GET the parent's
// /api/v1/hz-instances/<instance>/cert with the instance token, verify it
// (certdecide.go), write it atomically at 0600 into the HAProxy cert dir as
// letsencrypt.ParentCertFile, and call OnCertInstalled — which re-renders and
// reloads HAProxy — only when its sha256 changed.
//
// Boot never waits on this (#5): HAProxy is rendered from whatever bundle is
// on disk, and a parent that is down, refusing, or sending something that does
// not verify leaves that bundle in place.

// CertState is what the child knows of its certificate, for its UI.
type CertState struct {
	Installed   bool
	NotAfter    int64
	SANs        []string
	SHA256      string
	LastCheckAt int64
	LastError   string
}

type certMemo struct {
	mu       sync.Mutex
	state    CertState
	loudAt   time.Time
	loudLine string
}

// CertPath is where the parent's bundle is installed; "" when no cert dir is
// configured.
func (c *Child) CertPath() string {
	if c.CertDir == "" {
		return ""
	}
	return filepath.Join(c.CertDir, letsencrypt.ParentCertFile)
}

// PullsCert reports whether this child pulls a certificate at all.
func (c *Child) PullsCert() bool { return c.Instance != "" && c.CertDir != "" }

// readInstalled reads the installed bundle: its sha256 and what it is.
// Missing is ("", zero, nil).
func (c *Child) readInstalled() (string, CertState, error) {
	raw, err := os.ReadFile(c.CertPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", CertState{}, nil
	}
	if err != nil {
		return "", CertState{}, err
	}
	st := CertState{Installed: true, SHA256: BundleSHA256(raw)}
	if na, ok := letsencrypt.CertNotAfter(raw); ok {
		st.NotAfter = na.Unix()
	}
	if sans, ok := letsencrypt.CertSANs(raw); ok {
		st.SANs = sans
	}
	return st.SHA256, st, nil
}

// CertStatus is the current state for the UI: the installed bundle, read now,
// and the last attempt.
func (c *Child) CertStatus() CertState {
	if c.CertPath() == "" {
		return CertState{}
	}
	_, st, err := c.readInstalled()
	c.cert.mu.Lock()
	defer c.cert.mu.Unlock()
	st.LastCheckAt = c.cert.state.LastCheckAt
	st.LastError = c.cert.state.LastError
	if err != nil {
		st.LastError = "the installed certificate is unreadable: " + err.Error()
	}
	return st
}

// loudOnce logs a LOUD line, at most once an hour per distinct line — the
// pull runs every poll interval.
func (c *Child) loudOnce(line string, args ...any) {
	c.cert.mu.Lock()
	if line == c.cert.loudLine && time.Since(c.cert.loudAt) < time.Hour {
		c.cert.mu.Unlock()
		return
	}
	c.cert.loudLine, c.cert.loudAt = line, time.Now()
	c.cert.mu.Unlock()
	c.loud(line, args...)
}

func (c *Child) recordCert(err error) {
	c.cert.mu.Lock()
	defer c.cert.mu.Unlock()
	c.cert.state.LastCheckAt = time.Now().Unix()
	c.cert.state.LastError = ""
	if err != nil {
		c.cert.state.LastError = err.Error()
	}
}

// CheckInstalledCert is the start-up look at the installed bundle: an expired
// or unreadable one is LOUD, and kept.
func (c *Child) CheckInstalledCert() {
	if !c.PullsCert() {
		return
	}
	_, st, err := c.readInstalled()
	if err != nil {
		c.loud("nested hz cannot read its installed certificate; HAProxy keeps whatever it loaded", "path", c.CertPath(), "error", err)
		return
	}
	if line := InstalledLoud(st.NotAfter, time.Now().Unix()); line != "" {
		c.loud(line, "path", c.CertPath(), "not_after", time.Unix(st.NotAfter, 0).UTC().Format(time.RFC3339))
	}
}

// PullCertOnce is one conditional GET of the certificate, acted on.
func (c *Child) PullCertOnce(ctx context.Context) error {
	if !c.PullsCert() {
		return nil
	}
	err := c.pullCert(ctx)
	c.recordCert(err)
	return err
}

func (c *Child) pullCert(ctx context.Context) error {
	installedSHA, st, rerr := c.readInstalled()
	if rerr != nil {
		// Unreadable is not missing: do not write over a file nobody can read.
		c.loudOnce("nested hz cannot read its installed certificate; not replacing it", "path", c.CertPath(), "error", rerr)
		return rerr
	}
	keepLoud := func(why string) {
		if line := InstalledLoud(st.NotAfter, time.Now().Unix()); line != "" {
			c.loudOnce(line+" ("+why+")", "path", c.CertPath())
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := c.request(ctx, http.MethodGet, apitypes.HZInstancesPath+"/"+url.PathEscape(c.Instance)+apitypes.HZInstanceCertSuffix, nil)
	if err != nil {
		keepLoud("no instance token")
		return err
	}
	if installedSHA != "" {
		req.Header.Set("If-None-Match", `"`+installedSHA+`"`)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		keepLoud("parent unreachable")
		return fmt.Errorf("parent unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		keepLoud("parent has nothing newer")
		return nil
	case http.StatusOK:
	default:
		why := fmt.Sprintf("parent answered %d: %s", resp.StatusCode, errorBody(resp))
		keepLoud(why)
		return errors.New(why)
	}
	var body apitypes.InstanceCertResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return fmt.Errorf("unreadable certificate body: %w", err)
	}
	check, err := CheckCert(c.Instance, body, time.Now().Unix())
	if err != nil {
		c.loudOnce("nested hz REFUSED the certificate the parent sent and did not install it; the installed one (if any) keeps serving", "reason", err.Error())
		return fmt.Errorf("refused: %w", err)
	}
	if check.Loud != "" {
		c.loudOnce(check.Loud)
	}
	if !NeedsInstall(installedSHA, check) {
		return nil
	}
	if err := writeAtomic(c.CertPath(), check.Bundle); err != nil {
		c.loudOnce("nested hz verified a certificate from the parent but could not write it", "path", c.CertPath(), "error", err)
		return err
	}
	c.Log.Info("nested hz installed a certificate from the parent", "path", c.CertPath(), "sha256", check.SHA256,
		"not_after", time.Unix(check.NotAfter, 0).UTC().Format(time.RFC3339), "sans", check.SANs)
	if c.OnCertInstalled != nil {
		if err := c.OnCertInstalled(); err != nil {
			c.loudOnce("nested hz installed a certificate but HAProxy did not reload", "error", err)
			return fmt.Errorf("installed, but haproxy: %w", err)
		}
	}
	return nil
}
