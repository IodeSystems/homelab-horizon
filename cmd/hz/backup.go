package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/hzapi"
	"github.com/iodesystems/homelab-horizon/internal/backup"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

const backupUsage = `hz backup - pull and check hz's backup zip

The hz server never sends a backup anywhere: THIS machine pulls it, with your
credential, and keeps it. (An encrypted off-site copy is pushed by a root
command on the box: homelab-horizon backup push.) The zip holds config.json, the admin token, wireguard.conf,
invites, certificates, build logs and a snapshot of hz.db — which carries the
config manager's wrapped environment keys and sealed values. Those open only
with a machine private key or a recovery key, but treat the zip as a secret.
Release artifacts are NOT in it (manifest.json says so).

  hz backup pull [--dir DIR] [--keep N]
      Download the export, verify it (every sha256 in the manifest; the hz.db
      snapshot opens, integrity_check is ok, a schema version is recorded, the
      row counts match), write hz-backup-<host>-<UTC time>.zip (0600,
      atomically) and keep the newest N. Default --dir ~/backups/hz-<host>,
      --keep 14. Any failure exits non-zero and writes nothing.

  hz backup verify <zip>
      The same checks on a file you have, plus: would it restore into THIS
      binary (its schema version is not newer than this hz's latest migration).

  hz backup install-timer [--dir DIR] [--keep N] [--enable]
      Write a systemd USER service and timer on this machine that run
      'hz backup pull' daily (Persistent=true: a missed run happens at the next
      boot). Prints the unit paths. Enables the timer only with --enable.
`

func runBackup(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", backupUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "pull":
		return runBackupPull(c, args, os.Stdout)
	case "verify":
		return runBackupVerify(args, os.Stdout)
	case "install-timer":
		return runBackupInstallTimer(c, args, os.Stdout)
	case "-h", "--help", "help":
		fmt.Print(backupUsage)
		return nil
	}
	return fmt.Errorf("unknown backup subcommand: %s\n%s", sub, backupUsage)
}

type backupFlags struct {
	dir  string
	keep int
}

func parseBackupFlags(name string, args []string, extra func(*flag.FlagSet)) (backupFlags, *flag.FlagSet, error) {
	var bf backupFlags
	fs := flag.NewFlagSet("backup "+name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, backupUsage) }
	fs.StringVar(&bf.dir, "dir", "", "directory to keep backups in (default ~/backups/hz-<host>)")
	fs.IntVar(&bf.keep, "keep", 14, "how many backups of this host to keep")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return bf, fs, err
	}
	if fs.NArg() > 0 {
		return bf, fs, fmt.Errorf("unexpected argument %q\n%s", fs.Arg(0), backupUsage)
	}
	if bf.keep < 1 {
		return bf, fs, fmt.Errorf("--keep must be at least 1, got %d", bf.keep)
	}
	return bf, fs, nil
}

// backupHost is the name a backup is filed under: the host part of the URL hz
// is reached at, reduced to characters safe in a filename and a unit name.
func backupHost(hostURL string) string {
	h := hostURL
	if u, err := url.Parse(hostURL); err == nil && u.Hostname() != "" {
		h = u.Hostname()
	}
	h = regexp.MustCompile(`[^A-Za-z0-9.-]+`).ReplaceAllString(h, "_")
	if h == "" {
		h = "hz"
	}
	return h
}

func defaultBackupDir(host string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "backups", "hz-"+host), nil
}

// loudFail marks a failed pull so it cannot be mistaken for a quiet success in
// a journal: a timer that fails politely is a backup nobody has.
func loudFail(err error) error {
	return fmt.Errorf("LOUD: backup pull FAILED, nothing was written: %w", err)
}

func runBackupPull(c *client, args []string, out io.Writer) error {
	bf, _, err := parseBackupFlags("pull", args, nil)
	if err != nil {
		return err
	}
	data, err := c.getRaw("/admin/backup/export", 5*time.Minute)
	if err != nil {
		return loudFail(err)
	}
	v, err := backup.Verify(context.Background(), data)
	if err != nil {
		return loudFail(fmt.Errorf("the downloaded zip does not verify: %w", err))
	}

	host := backupHost(c.host)
	dir := bf.dir
	if dir == "" {
		if dir, err = defaultBackupDir(host); err != nil {
			return loudFail(err)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return loudFail(err)
	}
	name := fmt.Sprintf("hz-backup-%s-%s.zip", host, time.Now().UTC().Format("20060102T150405Z"))
	path := filepath.Join(dir, name)
	if err := backup.WriteFileAtomic(path, data); err != nil {
		return loudFail(err)
	}

	var rows int64
	for _, n := range v.Manifest.RowCounts {
		rows += n
	}
	_, _ = fmt.Fprintf(out, "wrote %s (%d bytes)\n", path, len(data))
	_, _ = fmt.Fprintf(out, "  hz %s, schema %d, snapshot %s, %d rows in %d tables, %d files\n",
		v.Manifest.HZVersion, v.Manifest.SchemaVersion, v.Manifest.SnapshotAt.Format(time.RFC3339),
		rows, len(v.Manifest.RowCounts), len(v.Manifest.Files))

	pruned, err := pruneBackups(dir, host, bf.keep)
	if err != nil {
		// The new backup is written and verified; a prune that failed is
		// still a failure the operator must see, or the disk fills quietly.
		return fmt.Errorf("LOUD: backup written to %s, but pruning failed: %w", path, err)
	}
	for _, p := range pruned {
		_, _ = fmt.Fprintf(out, "  pruned %s\n", p)
	}
	return nil
}

// pruneBackups keeps the newest `keep` backups of host in dir and deletes the
// rest. Only files named hz-backup-<host>-<time>.zip are considered, so a
// directory shared between hosts, or holding anything else, is safe. The UTC
// timestamp in the name sorts, so name order is age order.
func pruneBackups(dir, host string, keep int) ([]string, error) {
	pattern := regexp.MustCompile(`^hz-backup-` + regexp.QuoteMeta(host) + `-\d{8}T\d{6}Z\.zip$`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var mine []string
	for _, e := range entries {
		if e.Type().IsRegular() && pattern.MatchString(e.Name()) {
			mine = append(mine, e.Name())
		}
	}
	sort.Strings(mine)
	if len(mine) <= keep {
		return nil, nil
	}
	var pruned []string
	for _, n := range mine[:len(mine)-keep] {
		p := filepath.Join(dir, n)
		if err := os.Remove(p); err != nil {
			return pruned, err
		}
		pruned = append(pruned, p)
	}
	return pruned, nil
}

func runBackupVerify(args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: hz backup verify <zip>\n%s", backupUsage)
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	v, err := backup.Verify(context.Background(), data)
	if err != nil {
		return fmt.Errorf("LOUD: %s is NOT a good backup: %w", args[0], err)
	}
	latest, err := db.LatestMigration()
	if err != nil {
		return err
	}
	if err := db.CheckRestorable(v.Snapshot, latest); err != nil {
		return fmt.Errorf("LOUD: %s verifies but would NOT restore into this hz: %w", args[0], err)
	}
	_, _ = fmt.Fprintf(out, "ok: %s\n", args[0])
	_, _ = fmt.Fprintf(out, "  taken by hz %s at %s; %d files, every sha256 matches the manifest\n",
		v.Manifest.HZVersion, v.Manifest.SnapshotAt.Format(time.RFC3339), len(v.Manifest.Files))
	_, _ = fmt.Fprintf(out, "  hz.db: integrity ok, schema %d, row counts match (%d tables)\n",
		v.Snapshot.MigrationVersion, len(v.Snapshot.RowCounts))
	_, _ = fmt.Fprintf(out, "  would restore into this binary: schema %d <= %d\n", v.Snapshot.MigrationVersion, latest)
	for _, e := range v.Manifest.Excluded {
		_, _ = fmt.Fprintf(out, "  not in the zip: %s (%s)\n", e.Path, e.Reason)
	}
	return nil
}

// getRaw is an authenticated GET that returns the body as bytes, for the one
// endpoint that answers with a file rather than JSON.
func (c *client) getRaw(path string, timeout time.Duration) ([]byte, error) {
	if err := c.login(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, c.host+path, nil)
	if err != nil {
		return nil, err
	}
	hzapi.SetRequest(req.Header)
	if c.isPersonalToken() {
		req.Header.Set("Authorization", "Bearer "+c.token)
		if otp := strings.TrimSpace(os.Getenv("OTP")); otp != "" {
			req.Header.Set("X-HZ-OTP", otp)
		}
	}
	hc := *c.http
	hc.Timeout = timeout
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: reading the body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s -> %d: %s", path, resp.StatusCode, strings.TrimSpace(apiError(raw)))
	}
	return raw, nil
}

// --- install-timer ---

func runBackupInstallTimer(c *client, args []string, out io.Writer) error {
	var enable bool
	bf, _, err := parseBackupFlags("install-timer", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&enable, "enable", false, "also daemon-reload and enable --now the timer")
	})
	if err != nil {
		return err
	}
	cfg, err := loadConfig(c.flagHost, c.flagToken)
	if err != nil {
		return err
	}
	host := backupHost(cfg.Host)
	dir := bf.dir
	if dir == "" {
		if dir, err = defaultBackupDir(host); err != nil {
			return err
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find this hz binary: %w", err)
	}
	unitDir, err := userUnitDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}

	base := "hz-backup-" + host
	service, timer := backupUnits(exe, c.flagHost, os.Getenv("HZ_CONFIG"), dir, bf.keep, host)
	servicePath := filepath.Join(unitDir, base+".service")
	timerPath := filepath.Join(unitDir, base+".timer")
	if err := os.WriteFile(servicePath, []byte(service), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(timerPath, []byte(timer), 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote %s\nwrote %s\n", servicePath, timerPath)
	if c.flagToken != "" || os.Getenv("HZ_TOKEN") != "" {
		_, _ = fmt.Fprintln(out, "note: the unit does not carry a token from --token or HZ_TOKEN; it reads ~/.hz_config (or HZ_CONFIG)")
	}

	if !enable {
		_, _ = fmt.Fprintf(out, "not enabled. To enable:\n  systemctl --user daemon-reload\n  systemctl --user enable --now %s.timer\n", base)
		_, _ = fmt.Fprintf(out, "To run once now:  systemctl --user start %s.service\n", base)
		return nil
	}
	for _, argv := range [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", base + ".timer"},
	} {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = out, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
		}
	}
	_, _ = fmt.Fprintf(out, "enabled %s.timer. A user timer runs only while you are logged in unless lingering is on:\n  loginctl enable-linger %s\n", base, os.Getenv("USER"))
	return nil
}

func userUnitDir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("no home directory for the user unit")
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// backupUnits renders the service and timer. The token is never put in a
// unit: argv and unit files are readable by others; the pull reads
// ~/.hz_config like any other hz command.
func backupUnits(exe, flagHost, hzConfig, dir string, keep int, host string) (service, timer string) {
	argv := []string{exe}
	if flagHost != "" {
		argv = append(argv, "--host", flagHost)
	}
	argv = append(argv, "backup", "pull", "--dir", dir, "--keep", fmt.Sprint(keep))
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = systemdQuote(a)
	}
	var env string
	if hzConfig != "" {
		env = "Environment=" + systemdQuote("HZ_CONFIG="+hzConfig) + "\n"
	}
	service = fmt.Sprintf(`[Unit]
Description=Pull and verify hz's backup from %s (hz backup pull)
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
%sExecStart=%s
`, host, env, strings.Join(quoted, " "))
	timer = fmt.Sprintf(`[Unit]
Description=Daily hz backup pull from %s

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=15m

[Install]
WantedBy=timers.target
`, host)
	return service, timer
}

// systemdQuote quotes one ExecStart/Environment word: double quotes when it
// holds anything but plain characters, with \, " and % escaped (% is a unit
// specifier).
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if regexp.MustCompile(`^[A-Za-z0-9_./:=@+-]+$`).MatchString(s) {
		return s
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
