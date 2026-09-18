package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz cm push` seals a role's values locally and blesses a config from them.
//
// Three rules from plan/config-manager.md shape everything here, and each one
// exists because the obvious behaviour is the bug this project was started to
// kill:
//
//  1. Atomic per role, loud about what it skipped. A developer holding
//     staging/redline/app but not staging/redline/processor pushes the first
//     and is TOLD about the second. Never silently, never half a role.
//
//  2. Fail closed on the app's declared schema. A key in a file but absent
//     from the declared set is never pushed. An allowlist, not a discovery
//     pass — because the schema is reviewed in code review and the properties
//     file is not.
//
//  3. local.properties is structurally unpushable, and a key appearing in both
//     it and a pushed file is a HARD ERROR rather than a precedence rule.
//     Ambiguity about which value shipped is the bug class this exists to end.

// reservedRoles cannot be role names: the file convention is
// <role>.config.properties, so a role called "config" makes
// config.config.properties and config.properties ambiguous.
var reservedRoles = map[string]bool{"config": true, "secret": true, "local": true}

// cmSchema is the app's declared key set — the allowlist, and the source of
// each key's binding.
//
// It is a FILE rather than something hz serves, deliberately. The schema is
// what decides whether hz gets to see a key at all, so taking it from hz would
// let hz widen its own allowlist. It is what the app declares in code; this
// file is that declaration exported for the push tool.
//
//	{
//	  "app": "redline",
//	  "keys": { "DB_PASSWORD": "env", "RETENTION_DAYS": "invariant" },
//	  "roles": { "processor": { "BATCH_SIZE": "invariant" } }
//	}
//
// "roles" is optional and REPLACES "keys" for the roles it names, rather than
// extending it: a role whose declared set is a superset by accident is exactly
// the omission that lets an undeclared key travel.
type cmSchema struct {
	App   string                       `json:"app"`
	Keys  map[string]string            `json:"keys"`
	Roles map[string]map[string]string `json:"roles,omitempty"`
}

func loadCMSchema(path, app string) (*cmSchema, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the declared schema: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var s cmSchema
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if s.App != "" && app != "" && s.App != app {
		return nil, fmt.Errorf("%s declares app %q, but --app is %q", path, s.App, app)
	}
	if len(s.Keys) == 0 && len(s.Roles) == 0 {
		return nil, fmt.Errorf("%s declares no keys; an empty allowlist would push nothing, which is not what anybody means", path)
	}
	check := func(where string, keys map[string]string) error {
		for k, b := range keys {
			if k == "" {
				return fmt.Errorf("%s: %s declares an empty key name", path, where)
			}
			if b != configmgr.BindingInvariant && b != configmgr.BindingEnv {
				return fmt.Errorf("%s: key %s has binding %q, want %q or %q", path, k, b, configmgr.BindingInvariant, configmgr.BindingEnv)
			}
		}
		return nil
	}
	if err := check("the top level", s.Keys); err != nil {
		return nil, err
	}
	for role, keys := range s.Roles {
		if err := check("role "+role, keys); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

// forRole returns the declared set that governs one role.
func (s *cmSchema) forRole(role string) map[string]string {
	if keys, ok := s.Roles[role]; ok {
		return keys
	}
	return s.Keys
}

// --- properties ------------------------------------------------------------

// propEntry is one parsed line. Value is []byte so it can be zeroed after
// sealing; the strings a properties parser naturally produces cannot be.
type propEntry struct {
	Key   string
	Value []byte
	File  string
}

// parseProperties reads the subset of the java.util.Properties format that
// actually appears in a config file: comments with # or !, = or : as the
// separator, and a trailing backslash continuing onto the next line.
//
// Unicode \uXXXX escapes are NOT decoded, and that is a deliberate refusal
// rather than an omission: a value that means something different to this
// parser than to the app's is the exact failure this system exists to prevent,
// so a line containing one is an error, not a guess.
func parseProperties(path string) ([]propEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []propEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	var pending string
	var continued bool
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		// Leading whitespace is insignificant on a first line and on a
		// continuation alike, which is what java.util.Properties does; a comment
		// marker only starts a comment on a first line.
		trimmed := strings.TrimLeft(line, " \t")
		if !continued {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
				continue
			}
		}
		line = trimmed
		if strings.HasSuffix(line, `\`) && !strings.HasSuffix(line, `\\`) {
			pending += strings.TrimSuffix(line, `\`)
			continued = true
			continue
		}
		full := pending + line
		pending, continued = "", false

		sep := strings.IndexAny(full, "=:")
		if sep < 0 {
			return nil, fmt.Errorf("%s:%d: no '=' or ':' separator", path, lineNo)
		}
		key := strings.TrimSpace(full[:sep])
		value := strings.TrimSpace(full[sep+1:])
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty key", path, lineNo)
		}
		if strings.Contains(value, `\u`) || strings.Contains(key, `\u`) {
			return nil, fmt.Errorf("%s:%d: \\u escapes are not decoded here; a value this tool reads differently from the app is the bug this system exists to prevent. Write the literal character", path, lineNo)
		}
		out = append(out, propEntry{Key: key, Value: []byte(value), File: path})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if continued {
		return nil, fmt.Errorf("%s:%d: file ends with a line continuation", path, lineNo)
	}
	return out, nil
}

// propertyKeys returns only the NAMES in a file.
//
// This is what local.properties gets. The rule is that local.properties is
// never a push source, and it is not one here: no value from it is read into a
// variable that survives this function, nothing from it is ever sealed, and it
// is not in the file list a config is built from. Names alone are read because
// the collision rule — a key in both local.properties and a pushed file is a
// hard error — is otherwise unimplementable, and that rule is worth more than
// the literal reading of "do not read it at all".
func propertyKeys(path string) (map[string]bool, error) {
	entries, err := parseProperties(path)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(entries))
	for i := range entries {
		keys[entries[i].Key] = true
		zero(entries[i].Value)
		entries[i].Value = nil
	}
	return keys, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// --- push ------------------------------------------------------------------

func cmPush(c *client, args []string) error {
	fs := flag.NewFlagSet("cm push", flag.ContinueOnError)
	env := fs.String("env", "", "environment (required)")
	app := fs.String("app", "", "app (required)")
	var roles multiFlag
	fs.Var(&roles, "role", "role to push; repeatable (required)")
	schemaPath := fs.String("schema", "", "the app's declared key set, JSON (required)")
	dir := fs.String("dir", ".", "directory the per-role properties files are discovered in")
	defaultRole := fs.String("default-role", "app", "the role whose files are unprefixed (config.properties rather than <role>.config.properties)")
	minVer := fs.String("min-ver", "", "lowest app version this config serves (required)")
	maxVer := fs.String("max-ver", "", "highest app version this config serves; empty means open-ended")
	dryRun := fs.Bool("dry-run", false, "seal and report, but post nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *env == "":
		return fmt.Errorf("--env is required")
	case *app == "":
		return fmt.Errorf("--app is required")
	case len(roles) == 0:
		return fmt.Errorf("--role is required (repeat it to push several)")
	case *schemaPath == "":
		return fmt.Errorf("--schema is required: the declared key set is the allowlist, and without it a push is a discovery pass over whatever is in the file")
	case *minVer == "":
		return fmt.Errorf("--min-ver is required: ranges are immutable after blessing, so this is the only moment to get it right")
	}
	files := fs.Args()
	if len(files) > 0 && len(roles) > 1 {
		return fmt.Errorf("naming files explicitly only works for one --role; with several, files are discovered per role in --dir")
	}
	for _, r := range roles {
		if reservedRoles[r] {
			return fmt.Errorf("role %q is reserved: the file convention is <role>.config.properties, which %q makes ambiguous", r, r)
		}
	}

	schema, err := loadCMSchema(*schemaPath, *app)
	if err != nil {
		return err
	}
	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		return err
	}

	var failed []string
	for _, role := range roles {
		addr := configmgr.EnvKeyAddr{Environment: *env, App: *app, Role: role}
		roleFiles := files
		if len(roleFiles) == 0 {
			if roleFiles, err = discoverRoleFiles(*dir, role, *defaultRole); err != nil {
				fmt.Fprintf(os.Stderr, "SKIPPED %s: %v\n", addr, err)
				failed = append(failed, role)
				continue
			}
		}
		// The local file sits beside the files being pushed, which is not --dir
		// when files were named explicitly from somewhere else.
		localDir := *dir
		if len(files) > 0 {
			localDir = filepath.Dir(files[0])
		}
		if err := cmPushRole(c, ks, schema, addr, roleFiles, localDir, role, *defaultRole, *minVer, *maxVer, *dryRun); err != nil {
			// Loud, per role, and the loop continues: a developer who holds one
			// role's key and not another's pushes what they can and is told
			// plainly about the rest.
			fmt.Fprintf(os.Stderr, "SKIPPED %s: %v\n", addr, err)
			failed = append(failed, role)
			continue
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d role(s) were not pushed: %s", len(failed), len(roles), strings.Join(failed, ", "))
	}
	return nil
}

// discoverRoleFiles applies the naming convention: the default role's files are
// unprefixed, every other role's are <role>.-prefixed. local.properties is not
// in the list and never will be.
func discoverRoleFiles(dir, role, defaultRole string) ([]string, error) {
	prefix := role + "."
	if role == defaultRole {
		prefix = ""
	}
	var found []string
	for _, base := range []string{"config.properties", "secret.properties"} {
		p := filepath.Join(dir, prefix+base)
		if _, err := os.Stat(p); err == nil {
			found = append(found, p)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no %sconfig.properties or %ssecret.properties in %s", prefix, prefix, dir)
	}
	return found, nil
}

// localFilesFor names the files that must never be pushed and whose keys
// collide fatally with a pushed one: the role's own local file, and the bare
// one, which is machine-local regardless of role.
func localFilesFor(dir, role, defaultRole string) []string {
	out := []string{filepath.Join(dir, "local.properties")}
	if role != defaultRole {
		out = append(out, filepath.Join(dir, role+".local.properties"))
	}
	return out
}

// cmPushRole builds and posts one role's config, or fails having sent nothing.
//
// Every check runs before the first byte reaches hz. A role is a unit: a
// half-blessed config is a box booting on a set of values nobody chose.
func cmPushRole(c *client, ks *configmgr.Keystore, schema *cmSchema, addr configmgr.EnvKeyAddr,
	files []string, dir, role, defaultRole, minVer, maxVer string, dryRun bool,
) error {
	declared := schema.forRole(role)
	if len(declared) == 0 {
		return fmt.Errorf("the schema declares no keys for role %s", role)
	}

	// 1. Parse the pushable files. A key in two of them is ambiguous, and
	//    ambiguity is the thing being eliminated, so it is an error.
	seen := map[string]string{} // key -> file it came from
	var entries []propEntry
	defer func() {
		for i := range entries {
			zero(entries[i].Value)
		}
	}()
	for _, f := range files {
		if filepath.Base(f) == "local.properties" || strings.HasSuffix(filepath.Base(f), ".local.properties") {
			return fmt.Errorf("%s was named on the command line; local.properties is structurally unpushable", f)
		}
		parsed, err := parseProperties(f)
		if err != nil {
			return err
		}
		for _, e := range parsed {
			if prev, dup := seen[e.Key]; dup {
				return fmt.Errorf("%s is set in both %s and %s; which one would ship is exactly the question this refuses to answer", e.Key, prev, e.File)
			}
			seen[e.Key] = e.File
			entries = append(entries, e)
		}
	}

	// 2. local.properties: names only, and a collision is fatal.
	for _, lf := range localFilesFor(dir, role, defaultRole) {
		if _, err := os.Stat(lf); err != nil {
			continue
		}
		localKeys, err := propertyKeys(lf)
		if err != nil {
			return fmt.Errorf("checking %s for collisions: %w", lf, err)
		}
		var clashes []string
		for k := range localKeys {
			if src, ok := seen[k]; ok {
				clashes = append(clashes, fmt.Sprintf("%s (%s and %s)", k, src, lf))
			}
		}
		if len(clashes) > 0 {
			sort.Strings(clashes)
			return fmt.Errorf("these keys are set both locally and in a pushed file:\n  %s\n"+
				"that is a hard error, not a precedence rule: a local override of a pushed key means\n"+
				"what runs here is not what ships. Remove one of the two", strings.Join(clashes, "\n  "))
		}
	}

	// 3. The allowlist, in both directions. An undeclared key is skipped and
	//    reported; a declared key with no value fails the role, because a box
	//    that pulls a config missing a declared key falls back to a compiled
	//    default, which is the founding bug.
	var undeclared []string
	var push []propEntry
	for _, e := range entries {
		if _, ok := declared[e.Key]; !ok {
			undeclared = append(undeclared, e.Key)
			continue
		}
		push = append(push, e)
	}
	var missing []string
	for k := range declared {
		if _, ok := seen[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("declared but not present in %s: %s\n"+
			"a config missing a declared key makes the box fall back to its compiled default,\n"+
			"which is the failure this system exists to prevent. Supply them or narrow the schema",
			strings.Join(files, ", "), strings.Join(missing, ", "))
	}

	// 4. The key. Sealing consults hz's pointer and may refuse; that refusal is
	//    the point of the pointer, so it is surfaced with something to do.
	key, info, err := cmSealingKey(ks, c, addr)
	if err != nil {
		return err
	}

	// 5. Seal. The plaintext exists in this process and nowhere else.
	sort.Slice(push, func(i, j int) bool { return push[i].Key < push[j].Key })
	values := make([]apitypes.CMConfigValueReq, 0, len(push))
	for _, e := range push {
		values = append(values, apitypes.CMConfigValueReq{
			Key:     e.Key,
			Binding: declared[e.Key],
			Sealed:  configmgr.EncodeEnvelope(configmgr.Seal(key, valueAddr(addr, e.Key), e.Value)),
			KeyID:   info.ID.String(),
		})
	}

	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		fmt.Fprintf(os.Stderr, "%s: NOT PUSHED, not in the declared schema: %s\n", addr, strings.Join(undeclared, ", "))
	}
	fmt.Printf("%s: %d value(s) sealed under %s (%s), range %s\n", addr, len(values), info.ID, info.Label, verRange(minVer, maxVer))
	for _, v := range values {
		fmt.Printf("  %-28s %s\n", v.Key, v.Binding)
	}
	if dryRun {
		fmt.Printf("%s: --dry-run, nothing posted.\n", addr)
		return nil
	}

	req := apitypes.CMCreateConfigReq{
		Environment: addr.Environment,
		App:         addr.App,
		Role:        addr.Role,
		MinVer:      minVer,
		MaxVer:      maxVer,
		Values:      values,
	}
	var resp apitypes.CMConfigResp
	if err := c.do(http.MethodPost, cmAPI+"/configs", req, &resp); err != nil {
		return err
	}
	fmt.Printf("%s: blessed config %s (sequence %d).\n", addr, resp.ID, resp.Sequence)
	return nil
}

func verRange(minVer, maxVer string) string {
	if maxVer == "" {
		return minVer + "–∞"
	}
	return minVer + "–" + maxVer
}
