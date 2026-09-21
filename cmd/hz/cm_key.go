package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz cm key` is the only place environment keys are handled, and it handles
// them in exactly three ways: mint one into the keystore, print one to a
// terminal for a password manager, and read one back in from stdin.
//
// There is no flag anywhere here that takes key material. argv is world
// readable through /proc, so a --key flag would publish the highest-value
// secret in the system to every process on the box for the lifetime of the
// command. Import reads stdin; everything else reads the keystore.

func runCMKey(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cm key subcommand required: new | ls | export | import | current")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "new":
		return cmKeyNew(c, rest)
	case "ls", "list":
		return cmKeyList(rest)
	case "export":
		return cmKeyExport(rest)
	case "import":
		return cmKeyImport(rest)
	case "current":
		return cmKeyCurrent(c, rest)
	default:
		return fmt.Errorf("unknown cm key subcommand: %s", sub)
	}
}

// defaultKeyLabel is the year-month a key was minted in, which is what an
// operator reasons about during a rotation. The keystore's charset forbids a
// dot (that is what makes <label>.<keyid>.key parse), so the separator is a
// dash.
func defaultKeyLabel(now time.Time) string { return now.UTC().Format("2006-01") }

func cmKeyNew(c *client, args []string) error {
	fs := flag.NewFlagSet("cm key new", flag.ContinueOnError)
	label := fs.String("label", "", "human label for the key file (default: the current year-month)")
	setCurrent := fs.Bool("set-current", false, "also publish this key's ID as hz's current-key pointer for the address")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm key new <environment>/<app>/<role> [--label L] [--set-current]")
	}
	addr, err := parseCMAddr(pos)
	if err != nil {
		return err
	}
	if *label == "" {
		*label = defaultKeyLabel(time.Now())
	}

	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}
	// A second key at an address is a rotation, not a mistake, but it is not a
	// thing to do by accident either: until the pointer is moved, sealing
	// refuses while the newest key on disk and hz's pointer disagree.
	held, err := ks.List(keyAddr(addr))
	if err != nil {
		return err
	}

	key := configmgr.NewEnvKey()
	created := time.Now().UTC()
	if err := ks.Put(keyAddr(addr), *label, key, created); err != nil {
		return err
	}
	id := key.ID()
	fmt.Printf("Minted %s (%s) for %s.\n", id, *label, addr)
	fmt.Printf("  %s\n", filepath.Join(ks.Root(), "secrets", "keys", addr.Environment, addr.App, addr.Role, *label+"."+id.String()+".key"))
	fmt.Println()

	// Custody, automatically, at the one moment an environment key comes into
	// existence on a client. This is the whole reason the recovery recipient is
	// a LIST in hz's config rather than a step in a runbook: the recipient set
	// is fixed when a key is wrapped, so a key minted before somebody remembers
	// to wrap it is a key that has to be re-wrapped later, by hand, from this
	// exact machine. Nobody remembers.
	//
	// It is an ERROR, not a warning, when it fails. The key is already on disk,
	// so nothing is lost — but a key with no custody that reported success is
	// precisely the silent state this feature exists to make impossible, and
	// the message says what to run.
	wrapped, wrapErr := cmWrapToRecovery(c, addr, id.String(), key)
	switch {
	case wrapErr != nil:
		fmt.Printf("The key is on disk at the path above and is usable.\n\n")
		return fmt.Errorf("key %s was minted, but wrapping it to the recovery recipients failed: %w\n"+
			"  this key currently exists in ONE place: this machine's keystore.\n"+
			"  close that as soon as hz is reachable:  hz cm recovery backfill", id, wrapErr)
	case wrapped > 0:
		fmt.Printf("Wrapped to %d recovery recipient(s); hz now holds a blob only a recovery private\n", wrapped)
		fmt.Println("key can open. That a wrap exists is not proof it opens — prove it:")
		fmt.Printf("  hz cm recovery verify %s\n", addr)
	default:
		fmt.Println("No recovery recipients are registered, so this key exists in exactly one place:")
		fmt.Println("this machine's keystore. hz never holds an environment key, so there is nothing")
		fmt.Println("to restore from. Either set up custody, or back it up by hand:")
		fmt.Println("  hz cm recovery keygen --name <name>    # then 'add', then 'backfill'")
		fmt.Printf("  hz cm key export %s\n", addr)
	}

	if len(held) > 0 {
		fmt.Println()
		fmt.Printf("%d older key(s) already at this address. Nothing will seal under the new one\n", len(held))
		fmt.Println("until hz's current-key pointer names it, and boxes still holding an older key")
		fmt.Println("cannot open anything sealed under this one until they are re-approved.")
	}

	if *setCurrent {
		if err := cmSetCurrentKey(c, addr, id.String()); err != nil {
			return fmt.Errorf("key was minted, but publishing the pointer failed: %w", err)
		}
		fmt.Printf("\nhz now calls %s current for %s.\n", id, addr)
	} else if len(held) > 0 {
		fmt.Printf("\nWhen every holder has this key:  hz cm key current %s --set %s\n", addr, id)
	}
	return nil
}

// cmKeyList reports what this machine holds. Key IDs and labels only — the
// material is never rendered by anything but `export`, and then only to a
// terminal.
func cmKeyList(args []string) error {
	fs := flag.NewFlagSet("cm key ls", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output raw JSON")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm key ls [<environment>/<app>/<role>]")
	}
	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}

	var addrs []configmgr.EnvKeyAddr
	if pos != "" {
		a, err := parseCMAddr(pos)
		if err != nil {
			return err
		}
		addrs = []configmgr.EnvKeyAddr{a}
	} else {
		if addrs, err = cmKeyAddresses(ks); err != nil {
			return err
		}
	}

	type row struct {
		Address   string `json:"address"`
		ID        string `json:"id"`
		Label     string `json:"label"`
		CreatedAt string `json:"createdAt"`
	}
	var rows []row
	for _, a := range addrs {
		keys, err := ks.List(keyAddr(a))
		if err != nil {
			return fmt.Errorf("%s: %w", a, err)
		}
		for _, k := range keys {
			rows = append(rows, row{
				Address:   a.String(),
				ID:        k.ID.String(),
				Label:     k.Label,
				CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339),
			})
		}
	}

	if *asJSON {
		if rows == nil {
			rows = []row{}
		}
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(rows) == 0 {
		fmt.Printf("No keys in %s.\n", ks.Root())
		return nil
	}
	fmt.Printf("%-30s  %-16s  %-14s  %s\n", "ADDRESS", "KEY ID", "LABEL", "CREATED")
	for _, r := range rows {
		fmt.Printf("%-30s  %-16s  %-14s  %s\n", r.Address, r.ID, r.Label, r.CreatedAt)
	}
	return nil
}

// cmKeyAddresses enumerates the addresses the key tree holds something at.
//
// This walks directory NAMES with os.ReadDir, which the keystore itself never
// does, and the distinction matters: nothing found here is opened or trusted.
// Each candidate triple is handed back to Keystore.List, which re-validates
// every segment and re-walks the tree with openat(2) and its own permission
// checks. A directory whose name is not a legal segment is skipped rather than
// reported, because it is not a key by definition — the keystore could never
// have written it.
func cmKeyAddresses(ks *configmgr.Keystore) ([]configmgr.EnvKeyAddr, error) {
	root := filepath.Join(ks.Root(), "secrets", "keys")
	var out []configmgr.EnvKeyAddr
	envs, err := readDirNames(root)
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		apps, err := readDirNames(filepath.Join(root, env))
		if err != nil {
			return nil, err
		}
		for _, app := range apps {
			roles, err := readDirNames(filepath.Join(root, env, app))
			if err != nil {
				return nil, err
			}
			for _, role := range roles {
				out = append(out, configmgr.EnvKeyAddr{Environment: env, App: app, Role: role})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func readDirNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// cmKeyExport prints one key's text so it can go into a password manager.
//
// It refuses when stdout is not a terminal. That is not paternalism about
// pipes: a redirect is how key material lands in a file nobody set 0600 on, a
// shell history, a CI log, or a scrollback that gets pasted into a ticket. The
// legitimate machine-to-machine path is the keystore itself — copy the key
// FILE, which carries its own mode, its created_at and its label.
func cmKeyExport(args []string) error {
	fs := flag.NewFlagSet("cm key export", flag.ContinueOnError)
	id := fs.String("id", "", "which key to export, when the address holds more than one")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm key export <environment>/<app>/<role> [--id KEYID]")
	}
	addr, err := parseCMAddr(pos)
	if err != nil {
		return err
	}
	if !isTTY(os.Stdout) {
		return fmt.Errorf("refusing to export a key to something that is not a terminal.\n" +
			"  a redirect is how key material ends up in a file with the wrong mode, a shell\n" +
			"  history, or a CI log. Run this at a prompt and paste into your password manager.\n" +
			"  to move a key between machines, copy the key FILE instead: it carries its own\n" +
			"  0600, its created_at and its label")
	}

	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}
	held, err := ks.List(keyAddr(addr))
	if err != nil {
		return err
	}
	want, err := cmSelectKey(addr, held, *id)
	if err != nil {
		return err
	}
	key, err := ks.KeyForID(keyAddr(addr), want)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "# %s %s — the next line is the key. Nothing else on this machine has a copy\n", addr, want)
	fmt.Fprintf(os.Stderr, "# besides the key file itself, and hz has never had one.\n")
	fmt.Println(key.Text())
	// created_at is not in the key text, and it is what current-key resolution
	// turns on, so re-importing without it would silently re-date the key.
	for _, k := range held {
		if k.ID.String() == want {
			fmt.Fprintf(os.Stderr, "# store the created_at too; import needs it:\n")
			fmt.Fprintf(os.Stderr, "#   hz cm key import %s --label %s --created-at %s\n",
				addr, k.Label, k.CreatedAt.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

// cmSelectKey picks which held key an export means.
//
// An address holding several keys must name one. Guessing the newest would
// export the wrong key exactly during a rotation, which is the only time an
// address holds several and the only time an export is urgent.
func cmSelectKey(addr configmgr.EnvKeyAddr, held []configmgr.KeyInfo, id string) (string, error) {
	if len(held) == 0 {
		return "", fmt.Errorf("%w: nothing held for %s", configmgr.ErrNoSuchKey, addr)
	}
	if id != "" {
		for _, k := range held {
			if k.ID.String() == id {
				return id, nil
			}
		}
		return "", fmt.Errorf("%w: %s holds no key %s", configmgr.ErrNoSuchKey, addr, id)
	}
	if len(held) > 1 {
		lines := make([]string, 0, len(held))
		for _, k := range held {
			lines = append(lines, fmt.Sprintf("  %s  %s  created %s", k.ID, k.Label, k.CreatedAt.UTC().Format(time.RFC3339)))
		}
		return "", fmt.Errorf("%s holds %d keys; name one with --id:\n%s", addr, len(held), strings.Join(lines, "\n"))
	}
	return held[0].ID.String(), nil
}

// cmKeyImport reads key text from stdin. Stdin, because argv is public and a
// file path argument invites `--key-file <(echo ...)` which is argv again one
// level down.
func cmKeyImport(args []string) error {
	fs := flag.NewFlagSet("cm key import", flag.ContinueOnError)
	label := fs.String("label", "", "human label for the key file (default: the current year-month)")
	createdAt := fs.String("created-at", "", "RFC3339 created_at from the exporting machine (default: now)")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm key import <environment>/<app>/<role> [--label L] [--created-at T] < key.txt")
	}
	addr, err := parseCMAddr(pos)
	if err != nil {
		return err
	}
	if *label == "" {
		*label = defaultKeyLabel(time.Now())
	}
	created := time.Now().UTC()
	if *createdAt != "" {
		if created, err = time.Parse(time.RFC3339, *createdAt); err != nil {
			return fmt.Errorf("--created-at must be RFC3339: %w", err)
		}
		created = created.UTC()
	}

	if isTTY(os.Stdin) {
		fmt.Fprintln(os.Stderr, "Paste the key text, then press ctrl-D:")
	}
	// A whole-stream read, capped. The cap is generous next to a key's ~60
	// characters and exists only so a mistyped redirect cannot pull a large
	// file into memory.
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<16))
	if err != nil {
		return fmt.Errorf("reading the key from stdin: %w", err)
	}
	key, err := configmgr.ParseEnvKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return err
	}

	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}
	if err := ks.Put(keyAddr(addr), *label, key, created); err != nil {
		return err
	}
	fmt.Printf("Imported %s (%s) for %s, created %s.\n", key.ID(), *label, addr, created.Format(time.RFC3339))
	if *createdAt == "" {
		fmt.Println("created_at was not supplied, so it is now. If this key is older than another at")
		fmt.Println("the same address, it has just become the newest one on disk — which is the state")
		fmt.Println("the keystore refuses to seal in when hz's pointer says otherwise.")
	}
	return nil
}

// cmKeyCurrent reads or moves hz's advisory current-key pointer. Only an ID
// crosses the wire; hz has handled key IDs since the envelope format existed,
// so this discloses nothing it does not already index by.
func cmKeyCurrent(c *client, args []string) error {
	fs := flag.NewFlagSet("cm key current", flag.ContinueOnError)
	set := fs.String("set", "", "publish this key ID as current for the address")
	pos, rest := splitCMPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if pos == "" {
		pos = fs.Arg(0)
	}
	if pos == "" || fs.NArg() > 1 {
		return fmt.Errorf("usage: hz cm key current <environment>/<app>/<role> [--set KEYID]")
	}
	addr, err := parseCMAddr(pos)
	if err != nil {
		return err
	}
	if *set != "" {
		if _, err := configmgr.ParseKeyID(*set); err != nil {
			return fmt.Errorf("--set must be a key id: %w", err)
		}
		if err := cmSetCurrentKey(c, addr, *set); err != nil {
			return err
		}
		fmt.Printf("hz now calls %s current for %s.\n", *set, addr)
		fmt.Println("Boxes still holding an older key cannot open anything sealed under this one")
		fmt.Println("until they are re-approved. 'hz cm pending --all' shows which key each holds.")
		return nil
	}
	cur, err := cmCurrentKey(c, addr)
	if err != nil {
		return err
	}
	fmt.Printf("%s: hz says %s\n", addr, cur)
	return nil
}

func cmSetCurrentKey(c *client, addr configmgr.EnvKeyAddr, id string) error {
	q := url.Values{
		apitypes.CMQueryEnv:  {addr.Environment},
		apitypes.CMQueryApp:  {addr.App},
		apitypes.CMQueryRole: {addr.Role},
	}
	return c.do(http.MethodPut, cmAPI+"/current-key?"+q.Encode(), apitypes.CMCurrentKeyReq{KeyID: id}, nil)
}
