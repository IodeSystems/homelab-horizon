package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz instance` — the nested hz this one serves rungs to (config.HZInstance).
// An hz instance is NOT a machine: it has its own config, its credential is an
// instance token, and it never appears in `hz machine ls`.

const instanceUsage = `usage:
  hz instance ls
      The declared hz instances: name, owner, URL, VPN client, whether an
      instance token is minted.
  hz instance add <name> --url U [--project P] [--vpn-client C] [--note TEXT]
      Declare a nested hz. --url is where it answers (http or https with a
      host); this hz never contacts it. --vpn-client names an existing
      'upstream' VPN client it reaches this hz through.
  hz instance rm <name> [--cascade] [--confirm]
      Remove one. Dry run without --confirm. Refused while a rung's upstream
      names it or its VPN client exists; --cascade CLEARS each such upstream
      (the rung is kept) and removes the VPN client. Its token goes with it.
  hz instance token <name>
      Mint the instance token it pulls with. Shown ONCE; a re-mint replaces
      the old one.
  hz instance domains <name> <domain>... | --clear
      The hostnames it terminates TLS for. This hz issues ONE certificate
      covering exactly these (DNS-01 through its own zone's provider) and the
      instance pulls it with its token. All must be in one zone this hz owns
      with a DNS provider. --clear stops issuing; the child keeps what it has.

  hz machine hz-token <name>   DEPRECATED alias of hz instance token, one release.
`

const instanceTokenUsage = `usage: hz instance token <name>

Mints the instance token a nested hz (an hz instance) uses to pull from this
one: desired state, the applied artifact, and forwarded deploy reports — for
rungs whose upstream is <name>, and nothing else. Shown ONCE; hz keeps only
its sha256. A re-mint replaces it and the old token stops working. Put it in
the child's upstream.token_file (0600).
`

func runInstance(c *client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", instanceUsage)
	}
	switch args[0] {
	case "ls", "list":
		return instanceList(c)
	case "add":
		return instanceAdd(c, args[1:])
	case "rm", "remove":
		return instanceRm(c, args[1:])
	case "token":
		return instanceToken(c, args[1:])
	case "domains":
		return instanceDomains(c, args[1:])
	default:
		return fmt.Errorf("unknown instance subcommand: %s\n%s", args[0], instanceUsage)
	}
}

func instanceList(c *client) error {
	var out []apitypes.HZInstanceResp
	if err := c.do(http.MethodGet, apitypes.HZInstancesPath, nil, &out); err != nil {
		return err
	}
	if len(out) == 0 {
		fmt.Println("No hz instances. `hz nested bootstrap <project> <name> ...` or `hz instance add <name> --url U` declares one.")
		return nil
	}
	for _, h := range out {
		owner := h.Project
		if owner == "" {
			owner = "(global)"
		}
		vpn := h.VPNClient
		if vpn == "" {
			vpn = "(no VPN client)"
		}
		token := "token minted"
		if !h.HasToken {
			token = "NO TOKEN — `hz instance token " + h.Name + "`"
		}
		fmt.Printf("%-24s %-16s %-32s %-28s %s\n", h.Name, owner, h.URL, vpn, token)
		if len(h.Domains) > 0 {
			fmt.Printf("%-24s tls: %s\n", "", strings.Join(h.Domains, ", "))
		}
	}
	return nil
}

func instanceAdd(c *client, args []string) error {
	fs := flag.NewFlagSet("instance add", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, instanceUsage) }
	u := fs.String("url", "", "where the nested hz answers (http or https with a host)")
	project := fs.String("project", "", "the owning project; omit for global")
	vpn := fs.String("vpn-client", "", "an existing upstream VPN client it reaches this hz through")
	note := fs.String("note", "", "free text for the operator")
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *u == "" {
		return fmt.Errorf("%s", instanceUsage)
	}
	var out apitypes.HZInstanceResp
	req := apitypes.HZInstanceAddReq{Name: pos[0], Project: *project, URL: *u, VPNClient: *vpn, Note: *note}
	if err := c.do(http.MethodPost, apitypes.HZInstancesPath+"/add", req, &out); err != nil {
		return err
	}
	fmt.Printf("Declared hz instance %s at %s. Mint its token with `hz instance token %s`.\n", out.Name, out.URL, out.Name)
	return nil
}

func instanceRm(c *client, args []string) error {
	fs := flag.NewFlagSet("instance rm", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, instanceUsage) }
	cascade := fs.Bool("cascade", false, "clear each upstream naming it and remove its VPN client")
	confirm := fs.Bool("confirm", false, "actually remove it; without this it is a dry run")
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("%s", instanceUsage)
	}
	var out apitypes.RemovalResp
	req := apitypes.HZInstanceRmReq{Name: pos[0], Cascade: *cascade, Confirm: *confirm}
	if err := c.do(http.MethodPost, apitypes.HZInstancesPath+"/rm", req, &out); err != nil {
		return err
	}
	return renderRemoval("hz instance", pos[0], *cascade, *confirm, out)
}

func instanceToken(c *client, args []string) error {
	fs := flag.NewFlagSet("instance token", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, instanceTokenUsage) }
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("%s", instanceTokenUsage)
	}
	var out apitypes.InstanceTokenResp
	if err := c.do(http.MethodPost, apitypes.HZInstancesPath+"/token", apitypes.InstanceTokenReq{Instance: pos[0]}, &out); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Instance token for %s — shown once; hz keeps only its sha256. Put it in the child's upstream.token_file (0600):\n", out.Instance)
	fmt.Println(out.Token)
	return nil
}

// machineHZToken is the DEPRECATED `hz machine hz-token`: it says so and runs
// `hz instance token`.
func machineHZToken(c *client, args []string) error {
	fmt.Fprintln(os.Stderr, "hz machine hz-token is deprecated and goes in the next release: use hz instance token (an hz instance is not a machine)")
	return instanceToken(c, args)
}

func instanceDomains(c *client, args []string) error {
	fs := flag.NewFlagSet("instance domains", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, instanceUsage) }
	clearAll := fs.Bool("clear", false, "declare no domains: this hz stops issuing the instance a certificate")
	pos, err := positional(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || (*clearAll && len(pos) != 1) || (!*clearAll && len(pos) < 2) {
		return fmt.Errorf("%s", instanceUsage)
	}
	domains := append([]string{}, pos[1:]...)
	var out apitypes.HZInstanceResp
	if err := c.do(http.MethodPost, apitypes.HZInstancesPath+"/set", apitypes.HZInstanceSetReq{Name: pos[0], Domains: &domains}, &out); err != nil {
		return err
	}
	if len(out.Domains) == 0 {
		fmt.Printf("hz instance %s declares no domains; no certificate is issued for it. The child keeps the one it has.\n", out.Name)
		return nil
	}
	fmt.Printf("hz instance %s: this hz issues one certificate for %s (in the background; see its Instances page). The child pulls it with its token.\n",
		out.Name, strings.Join(out.Domains, ", "))
	return nil
}
