package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// Putting a service in the tree, from the command line.
//
// `hz project add` and `hz env add` declare the tree; this is the verb that
// points a service at it, and step 2 of the acceptance walkthrough in
// plan/architecture.md — "assign a service to it" — had no command until it
// landed. The only thing that ever wrote Service.Project was `hz import
// --execute`, so a service created after an import could never join a project
// and one the import placed could never be moved or taken off.
//
// ITS OWN VERB, not a flag on `hz service edit`, for two reasons.
//
//	It is a different axis. `service edit`'s thirty flags are all about what the
//	service IS — domains, backend, TLS, forwards, timeouts. Where it sits in the
//	tree is what `project add` and `env add` write, they address it as
//	<project>/<name>, and matching that spelling makes the walkthrough three
//	commands that read as one sentence. Nobody reading "assign a service to it"
//	goes looking under `service edit --project`.
//
//	It is a narrower write. `service edit` is full-replace: it re-sends the
//	domains, proxy, DNS and forwards it round-tripped from a read. That is the
//	right risk for changing a backend and the wrong one for moving a service
//	between rungs — an assign should not be able to drop a port forward somebody
//	added while the read was in flight. /api/v1/services/assign writes two
//	fields.
//
// UNASSIGN IS ITS OWN VERB TOO, rather than `assign x ""`. `env set` needs
// fs.Visit because a rung's three fields are independent and absent has to mean
// "leave it alone"; a placement is ONE fact with two halves — an environment
// means nothing without a project — so there is no partial assign to express,
// and an empty positional argument is a typo far more often than it is an
// intention.

const serviceAssignUsage = `usage: hz service assign <service> <project>[/<environment>]

Puts a service in the project tree. The project — and the rung, if you name one
— must already be declared: hz refuses a service naming one that is not, so the
order is declare, THEN assign.

  hz service assign app redline            # in the project, on no rung
  hz service assign app redline/staging    # in the project, on the staging rung

A project with no environment is legal and common: plenty of services are not on
a ladder at all. An environment with no project is not — an environment name is
unique per project, not globally.

Writes immediately, and renders nothing: assignment moves no DNS record and no
HAProxy backend, so there is nothing to sync. 'hz service unassign <service>'
takes it back out.
`

func serviceAssign(c *client, args []string) error {
	fs := flag.NewFlagSet("service assign", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, serviceAssignUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("%s", serviceAssignUsage)
	}
	service := fs.Arg(0)
	project, environment, err := splitAssignAddr(fs.Arg(1))
	if err != nil {
		return err
	}

	var out apitypes.ServiceAssignResp
	req := apitypes.ServiceAssignReq{Service: service, Project: project, Environment: environment}
	if err := c.do(http.MethodPost, "/api/v1/services/assign", req, &out); err != nil {
		return err
	}
	printAssignment(out)
	return nil
}

const serviceUnassignUsage = `usage: hz service unassign <service>

Takes a service out of the project tree. It keeps every domain, backend, forward
and certificate it has and goes on working exactly as it did — an unassigned
service is the state every service was in before projects existed, and it is
legal. All it loses is its place in 'hz project ls', where it moves to
(unassigned).

'hz service assign <service> <project>[/<environment>]' puts it back.
`

func serviceUnassign(c *client, args []string) error {
	fs := flag.NewFlagSet("service unassign", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, serviceUnassignUsage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%s", serviceUnassignUsage)
	}

	var out apitypes.ServiceAssignResp
	req := apitypes.ServiceAssignReq{Service: fs.Arg(0)}
	if err := c.do(http.MethodPost, "/api/v1/services/assign", req, &out); err != nil {
		return err
	}
	printAssignment(out)
	return nil
}

// splitAssignAddr parses the <project>[/<environment>] address.
//
// The environment half is OPTIONAL here and required by `hz env add`, which is
// the difference between declaring a rung and pointing at one: a service in a
// project and on no rung is a real and common shape, so a bare project name has
// to mean that rather than be a half-typed address.
func splitAssignAddr(addr string) (project, environment string, err error) {
	project, environment, ok := strings.Cut(addr, "/")
	if project == "" {
		return "", "", fmt.Errorf("%q names no project — an assignment needs one, and an environment name is unique per project, not globally\n%s",
			addr, serviceAssignUsage)
	}
	if ok && environment == "" {
		return "", "", fmt.Errorf("%q has a trailing slash and no environment — write %q for the project alone, or %s/<environment> for a rung in it",
			addr, project, project)
	}
	return project, environment, nil
}

// assignmentLine renders a placement for `hz service show`. An unassigned
// service says so and says what to type, rather than leaving a blank the reader
// has to interpret.
func assignmentLine(project, environment string) string {
	switch {
	case project == "":
		return "none — unassigned (hz service assign <service> <project>[/<environment>])"
	case environment == "":
		return project + " (on no environment)"
	default:
		return project + "/" + environment
	}
}

// printAssignment reports the placement hz now holds, in the same words `hz
// service show` uses for it, so a write and a read of the same record look the
// same.
func printAssignment(out apitypes.ServiceAssignResp) {
	switch {
	case out.Project == "":
		fmt.Printf("Service %s is now unassigned.\n", out.Service)
		fmt.Println("  It keeps its domains, backend and certificate, and goes on working.")
		fmt.Printf("  `hz service assign %s <project>` puts it back in the tree.\n", out.Service)
	case out.Environment == "":
		fmt.Printf("Service %s is now in project %s, on no environment.\n", out.Service, out.Project)
		fmt.Printf("  That is legal: not every service is on a ladder. `hz service assign %s %s/<environment>`\n", out.Service, out.Project)
		fmt.Println("  puts it on a rung once one is declared.")
	default:
		fmt.Printf("Service %s is now on %s/%s.\n", out.Service, out.Project, out.Environment)
		fmt.Printf("  `hz env show %s/%s` lists everything on that rung.\n", out.Project, out.Environment)
	}
}
