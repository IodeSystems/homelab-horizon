package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/backup"
	"github.com/iodesystems/homelab-horizon/internal/backup/offsite"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/server"
)

// Off-site, encrypted backups of THIS box's hz (internal/backup/offsite).
//
// push runs on the box, as root, at the console or from a root timer. It
// builds the same verified zip `hz backup pull` downloads, in-process, encrypts
// it to every recovery recipient and PUTs it to a bucket. hz initiates nothing
// toward another machine it manages (CLAUDE.md #1): this is an outbound write
// to storage with a put-only principal. decrypt and restore are the way back;
// the download between them is the operator's, with their own session.

func newBackupCmd(opts *serveOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Encrypted off-site backups of this box's hz (push, decrypt, restore)",
		Long: "push encrypts this hz's backup zip (config, token, wireguard.conf, certs,\n" +
			"build logs and an hz.db snapshot) to every recovery recipient and uploads\n" +
			"it with s3:PutObject only. hz never holds a credential that reads the\n" +
			"bucket: restores download with the operator's own session, then\n" +
			"`backup decrypt` with the offline recovery key, then `backup restore`.",
	}
	cmd.AddCommand(newBackupPushCmd(opts), newBackupInstallPushTimerCmd(opts),
		newBackupDecryptCmd(), newBackupRestoreCmd(opts))
	return cmd
}

// pushFlags are push's flags, shared with the timer that runs it.
type pushFlags struct {
	dest       string
	awsProfile string
	region     string
}

func (p *pushFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p.dest, "dest", "", "s3://<bucket>/<prefix>; objects go to <prefix>/<host>/<UTC time>.hzbak and .json (required)")
	cmd.Flags().StringVar(&p.awsProfile, "aws-profile", "", "AWS shared-config profile (default $AWS_PROFILE, else "+offsite.DefaultProfile+")")
	cmd.Flags().StringVar(&p.region, "region", "", "the bucket's region (default: the profile's region)")
}

func (p pushFlags) profile() string {
	if p.awsProfile != "" {
		return p.awsProfile
	}
	if v := os.Getenv("AWS_PROFILE"); v != "" {
		return v
	}
	return offsite.DefaultProfile
}

func newBackupPushCmd(opts *serveOpts) *cobra.Command {
	var pf pushFlags
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Encrypt this hz's backup to its recovery recipients and upload it (root)",
		Long: "Build the verified backup zip in-process, encrypt it client-side to EVERY\n" +
			"recovery recipient in this hz's config (refused when there is none), and\n" +
			"upload the ciphertext, then a cleartext header (.json), with s3:PutObject.\n\n" +
			"Credentials come from the AWS SDK's default chain with --aws-profile (or\n" +
			"AWS_PROFILE) selecting the profile: a static-key profile and an assumed-role\n" +
			"profile (role_arn + credential_source) both work. Never from hz's config.\n\n" +
			"The upload is verified without reading it back: S3's returned checksum must\n" +
			"equal the sha256 of the local ciphertext. Any failure exits non-zero.",
		Example: "  homelab-horizon backup push --dest s3://example-hz-backups/hz-office",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return errors.New("backup push reads hz.db, the admin token and the private keys in wireguard.conf: run it as root on the box")
			}
			res, err := runBackupPush(cmd.Context(), opts.configPath, pf, time.Now())
			if err != nil {
				return fmt.Errorf("LOUD: off-site backup push FAILED: %w", err)
			}
			printPush(cmd.OutOrStdout(), pf, res)
			return nil
		},
	}
	pf.register(cmd)
	_ = cmd.MarkFlagRequired("dest")
	return cmd
}

func printPush(out io.Writer, pf pushFlags, res *offsite.PushResult) {
	d, _ := offsite.ParseDest(pf.dest)
	h := res.Header
	_, _ = fmt.Fprintf(out, "pushed s3://%s/%s (%d bytes, sha256 %s; S3's checksum matches)\n", d.Bucket, res.Object, h.CiphertextBytes, h.CiphertextSHA256)
	_, _ = fmt.Fprintf(out, "pushed s3://%s/%s\n", d.Bucket, res.Sidecar)
	_, _ = fmt.Fprintf(out, "  hz %s on %s, schema %d, snapshot %s\n", h.HZVersion, h.Host, h.SchemaVersion, h.SnapshotAt.Format(time.RFC3339))
	for _, r := range h.Recipients {
		_, _ = fmt.Fprintf(out, "  opens with recovery key %s (%s)\n", r.Name, r.Fingerprint)
	}
}

// runBackupPush is push without the root check, for tests.
func runBackupPush(ctx context.Context, configPath string, pf pushFlags, now time.Time, s3opts ...func(*offsite.PushDeps)) (*offsite.PushResult, error) {
	dest, err := offsite.ParseDest(pf.dest)
	if err != nil {
		return nil, err
	}
	cfg, cfgPath, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	// Refuse before any work: nobody to encrypt to means nothing is uploaded.
	recipients, err := offsite.RecipientsFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	deps := offsite.PushDeps{}
	for _, o := range s3opts {
		o(&deps)
	}
	if deps.API == nil {
		awsCfg, err := offsite.LoadAWSConfig(ctx, pf.profile(), pf.region)
		if err != nil {
			return nil, err
		}
		deps.API = offsite.NewS3Client(awsCfg)
	}

	token := cfg.AdminToken
	if b, err := os.ReadFile(cfgPath + ".token"); err == nil && strings.TrimSpace(string(b)) != "" {
		token = strings.TrimSpace(string(b))
	}
	store, err := db.OpenExisting(cfg.UsersDBPath())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cfg.UsersDBPath(), err)
	}
	defer func() { _ = store.Close() }()

	zipData, m, err := server.BuildBackupZip(ctx, server.ExportSource{
		Config:     cfg,
		AdminToken: token,
		BuildDir:   filepath.Join(cfg.DataDir(), "builds"),
		DB:         store,
		HZVersion:  Version,
	})
	if err != nil {
		return nil, fmt.Errorf("build the backup: %w", err)
	}
	// The same whole-zip check `hz backup pull` runs on what it downloads.
	if _, err := backup.Verify(ctx, zipData); err != nil {
		return nil, fmt.Errorf("the backup zip does not verify: %w", err)
	}
	meta := offsite.Meta{HZVersion: m.HZVersion, Host: m.Host, SchemaVersion: m.SchemaVersion, SnapshotAt: m.SnapshotAt}
	return offsite.Push(ctx, deps.API, dest, zipData, meta, recipients, now, deps.TmpDir)
}

// --- install-push-timer ---

const pushUnitBase = "hz-backup-push"

func newBackupInstallPushTimerCmd(opts *serveOpts) *cobra.Command {
	var pf pushFlags
	var enable bool
	var unitDir string
	cmd := &cobra.Command{
		Use:   "install-push-timer",
		Short: "Write a root systemd service and daily timer that run backup push",
		Long: "Write " + pushUnitBase + ".service and .timer: daily, Persistent=true (a missed\n" +
			"run happens at the next boot), with a randomised delay. The service sets\n" +
			"AWS_PROFILE and reads /root/.aws/config and /root/.aws/credentials.\n" +
			"Enables the timer only with --enable.",
		Example: "  homelab-horizon backup install-push-timer --dest s3://example-hz-backups/hz-office --enable",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := offsite.ParseDest(pf.dest); err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find this binary: %w", err)
			}
			if exe, err = filepath.Abs(exe); err != nil {
				return err
			}
			cfgPath := opts.configPath
			if cfgPath != "" {
				if cfgPath, err = filepath.Abs(cfgPath); err != nil {
					return err
				}
			}
			service, timer := backupPushUnits(exe, cfgPath, pf)
			out := cmd.OutOrStdout()
			for name, body := range map[string]string{pushUnitBase + ".service": service, pushUnitBase + ".timer": timer} {
				p := filepath.Join(unitDir, name)
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(out, "wrote %s\n", p)
			}
			if !enable {
				_, _ = fmt.Fprintf(out, "not enabled. To enable:\n  systemctl daemon-reload\n  systemctl enable --now %s.timer\n", pushUnitBase)
				_, _ = fmt.Fprintf(out, "To run once now:  systemctl start %s.service; journalctl -u %s.service\n", pushUnitBase, pushUnitBase)
				return nil
			}
			for _, argv := range [][]string{
				{"systemctl", "daemon-reload"},
				{"systemctl", "enable", "--now", pushUnitBase + ".timer"},
			} {
				c := exec.Command(argv[0], argv[1:]...)
				c.Stdout, c.Stderr = out, os.Stderr
				if err := c.Run(); err != nil {
					return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
				}
			}
			_, _ = fmt.Fprintf(out, "enabled %s.timer\n", pushUnitBase)
			return nil
		},
	}
	pf.register(cmd)
	_ = cmd.MarkFlagRequired("dest")
	cmd.Flags().BoolVar(&enable, "enable", false, "also daemon-reload and enable --now the timer")
	cmd.Flags().StringVar(&unitDir, "unit-dir", "/etc/systemd/system", "where to write the units")
	return cmd
}

// backupPushUnits renders the root service and timer. No credential is in
// either: AWS_PROFILE names a profile in root's shared config, which the SDK
// reads at run time.
func backupPushUnits(exe, configPath string, pf pushFlags) (service, timer string) {
	argv := []string{exe}
	if configPath != "" {
		argv = append(argv, "--config", configPath)
	}
	argv = append(argv, "backup", "push", "--dest", pf.dest)
	if pf.region != "" {
		argv = append(argv, "--region", pf.region)
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = systemdQuote(a)
	}
	service = fmt.Sprintf(`[Unit]
Description=Encrypt hz's backup and push it off-site to %s (homelab-horizon backup push)
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
Environment=%s
Environment=AWS_CONFIG_FILE=/root/.aws/config
Environment=AWS_SHARED_CREDENTIALS_FILE=/root/.aws/credentials
ExecStart=%s
PrivateTmp=yes
NoNewPrivileges=yes
`, pf.dest, systemdQuote("AWS_PROFILE="+pf.profile()), strings.Join(quoted, " "))
	timer = fmt.Sprintf(`[Unit]
Description=Daily off-site hz backup push to %s

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
`, pf.dest)
	return service, timer
}

// systemdQuote quotes one ExecStart/Environment word: double quotes when it
// holds anything but plain characters, with \, " and % escaped (% is a unit
// specifier). The same rule as cmd/hz's.
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if regexp.MustCompile(`^[A-Za-z0-9_./:=@+-]+$`).MatchString(s) {
		return s
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// --- decrypt ---

func newBackupDecryptCmd() *cobra.Command {
	var keyPath, outPath string
	cmd := &cobra.Command{
		Use:   "decrypt <file.hzbak> --recovery-key <file|->",
		Short: "Decrypt an off-site backup with an offline recovery key, to a zip (0600)",
		Long: "Decrypt a .hzbak with the recovery PRIVATE key `hz config recovery keygen`\n" +
			"printed (one line, hzmprv_...). --recovery-key - reads it from stdin. The\n" +
			"zip is written only when every chunk authenticates, then checked like\n" +
			"`hz backup verify`. A <name>.json header beside the file is checked too.\n\n" +
			"Runs anywhere; it needs no hz, no network and no AWS credential.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupDecrypt(cmd.Context(), args[0], keyPath, outPath, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&keyPath, "recovery-key", "", "file holding the recovery private key, or - for stdin (required)")
	cmd.Flags().StringVar(&outPath, "out", "", "the zip to write (default <file>.zip beside it)")
	_ = cmd.MarkFlagRequired("recovery-key")
	return cmd
}

// stdinIsTerminal reports whether stdin is a terminal; tests replace it.
var stdinIsTerminal = func() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// readRecoveryKeyFromStdin reads the pasted key. At a terminal it prompts on
// stderr and reads ONE line — reading to EOF looked hung until Ctrl-D (operator
// report, 2026-10-09). A pipe or redirect is read to EOF as before. The key is
// never echoed back or logged.
func readRecoveryKeyFromStdin(stdin io.Reader, tty bool, prompt io.Writer) ([]byte, error) {
	limited := io.LimitReader(stdin, 1<<16)
	if !tty {
		return io.ReadAll(limited)
	}
	fmt.Fprint(prompt, "Paste the recovery private key (hzmprv_…), then press Enter: ")
	line, err := bufio.NewReader(limited).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return nil, err
	}
	return []byte(line), nil
}

func runBackupDecrypt(ctx context.Context, in, keyPath, outPath string, stdin io.Reader, out io.Writer) error {
	var raw []byte
	var err error
	if keyPath == "-" {
		raw, err = readRecoveryKeyFromStdin(stdin, stdinIsTerminal(), os.Stderr)
	} else {
		raw, err = os.ReadFile(keyPath)
	}
	if err != nil {
		return fmt.Errorf("read the recovery key: %w", err)
	}
	priv, err := configmgr.ParseMachinePrivateKey(strings.TrimSpace(string(raw)))
	if err != nil {
		// The error names the expected form, never the bytes.
		return fmt.Errorf("that is not a recovery private key: %w", err)
	}

	if outPath == "" {
		outPath = strings.TrimSuffix(in, offsite.ObjectSuffix) + ".zip"
	}
	if _, err := os.Stat(outPath); err == nil {
		return fmt.Errorf("%s exists; refusing to overwrite it (pass --out)", outPath)
	}
	src, err := os.Open(in)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	sum := sha256.New()

	tmp, err := os.CreateTemp(filepath.Dir(outPath), "."+filepath.Base(outPath)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	h, err := offsite.Decrypt(io.TeeReader(src, sum), priv, tmp)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("LOUD: %s does NOT decrypt, nothing was written: %w", in, err)
	}
	if err := os.Rename(tmp.Name(), outPath); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote %s (0600)\n", outPath)
	_, _ = fmt.Fprintf(out, "  hz %s on %s, schema %d, snapshot %s\n", h.Meta.HZVersion, h.Meta.Host, h.Meta.SchemaVersion, h.Meta.SnapshotAt.Format(time.RFC3339))

	// The cleartext header, when the operator downloaded it too.
	gotSum := hex.EncodeToString(sum.Sum(nil))
	sidePath := strings.TrimSuffix(in, offsite.ObjectSuffix) + offsite.SidecarSuffix
	if b, err := os.ReadFile(sidePath); err == nil {
		var side offsite.Sidecar
		if err := json.Unmarshal(b, &side); err != nil {
			return fmt.Errorf("LOUD: %s is not a backup header: %w", sidePath, err)
		}
		if side.CiphertextSHA256 != gotSum || side.BackupID != h.BackupID {
			return fmt.Errorf("LOUD: %s describes a different object (sha256 %s, backup %s; this file is %s, %s)",
				sidePath, side.CiphertextSHA256, side.BackupID, gotSum, h.BackupID)
		}
		_, _ = fmt.Fprintf(out, "  %s matches: sha256 %s\n", sidePath, gotSum)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return err
	}
	v, err := backup.Verify(ctx, data)
	if err != nil {
		return fmt.Errorf("LOUD: %s decrypted, but is NOT a good backup: %w", outPath, err)
	}
	_, _ = fmt.Fprintf(out, "  verified: every sha256 matches the manifest; hz.db integrity ok, schema %d, %d tables\n",
		v.Snapshot.MigrationVersion, len(v.Snapshot.RowCounts))
	_, _ = fmt.Fprintf(out, "next: homelab-horizon backup restore %s\n", outPath)
	return nil
}

// --- restore ---

func newBackupRestoreCmd(opts *serveOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "restore <zip>",
		Short: "Stage a decrypted backup zip for the next start (hz stopped, root)",
		Long: "Write config.json, the admin token, wireguard.conf, invites and certificates\n" +
			"from the zip, and stage hz.db as <db>.restore — the same import path as\n" +
			"POST /admin/backup/import, with its checks (every sha256, integrity,\n" +
			"row counts, a schema not newer than this binary's) run before anything is\n" +
			"written. hz swaps the database in at its next start, keeping the old one\n" +
			"as <db>.pre-restore-<time>.\n\n" +
			"Refused while homelab-horizon.service is active: a running hz would save\n" +
			"its own config over the restored one.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() != 0 {
				return errors.New("backup restore writes /etc and /var/lib: run it as root on the box")
			}
			if exec.Command("systemctl", "is-active", "--quiet", "homelab-horizon").Run() == nil {
				return errors.New("homelab-horizon.service is running; stop it first (systemctl stop homelab-horizon), or the running hz will save its config over the restored one")
			}
			return runBackupRestore(cmd.Context(), opts.configPath, args[0], cmd.OutOrStdout())
		},
	}
}

func runBackupRestore(ctx context.Context, configPath, zipPath string, out io.Writer) error {
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return err
	}
	files, err := server.UnpackBackupZip(data)
	if err != nil {
		return err
	}
	if configPath == "" {
		configPath, _ = config.Find()
	}
	res, err := server.ImportBackupFiles(ctx, files, server.ImportTarget{
		ConfigPath: configPath,
		DBPath:     func(c *config.Config) string { return c.UsersDBPath() },
		BuildDir:   func(c *config.Config) string { return filepath.Join(c.DataDir(), "builds") },
	})
	if err != nil {
		return fmt.Errorf("LOUD: restore refused, nothing was written: %w", err)
	}
	if len(res.Errors) > 0 {
		var b bytes.Buffer
		for _, e := range res.Errors {
			b.WriteString("\n  " + e)
		}
		return fmt.Errorf("LOUD: restore PARTIAL (database staged: %v):%s", res.RestartRequired, b.String())
	}
	_, _ = fmt.Fprintf(out, "restored config to %s\n", configPath)
	if !res.RestartRequired {
		_, _ = fmt.Fprintln(out, "this zip carries no hz.db; nothing is staged")
		return nil
	}
	_, _ = fmt.Fprintf(out, "staged hz.db (schema %d, taken by hz %s at %s); it is swapped in at the next start\n",
		res.Verified.Snapshot.MigrationVersion, res.Verified.Manifest.HZVersion, res.Verified.Manifest.SnapshotAt.Format(time.RFC3339))
	_, _ = fmt.Fprintln(out, "next:\n  systemctl start homelab-horizon\n  hz config recovery verify <project>/<environment>/<app>/<role> < recovery-key.txt")
	return nil
}
