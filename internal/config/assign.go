package config

import (
	"fmt"
	"strings"
)

// Assigning a service to the tree, which is the third verb of the walkthrough
// and the one that had no command.
//
// declare.go writes the tree; this writes the pointer INTO it. Until this
// landed the only thing that ever set Service.Project/Service.Environment was
// `hz import --execute`, so a service created after an import could never join
// a project and one the import placed could never be moved or taken off.
//
// The order is the one legacy_compat_test.go pins: DECLARE, THEN ASSIGN. Save
// refuses a service naming a project or a rung nobody declared, and that
// refusal reads as `service "x" names environment "y", which does not exist` —
// true, and useless to an operator who does not yet know that declaring is a
// separate step. CheckAssignment is here to refuse the same thing and NAME THE
// COMMAND that fixes it, before anything is written.

// CheckAssignment reports whether a service may name this project and rung.
//
// Three outcomes, and the middle one is the whole reason this is not just a
// zero-value test:
//
//   - project "" and environment "": UNASSIGNED, always legal. It is the state
//     every service was in before the projects slice existed and it keeps the
//     service working exactly as it does now.
//   - project set, environment "": legal. A service can sit in a project and on
//     no rung — plenty of services are not on a ladder at all, and requiring a
//     rung would force a fake one.
//   - environment set with no project: refused. An environment name is unique
//     per project, not globally, so a rung with no project resolves against
//     nothing; ValidateEnvironments deliberately does not even look at it, which
//     means storing it would leave a field that silently means nothing.
//
// It takes the service NAME only to put it in the message — the service need not
// exist yet, which is what lets the add handler run the same check.
func (c *Config) CheckAssignment(service, project, environment string) error {
	project = strings.TrimSpace(project)
	environment = strings.TrimSpace(environment)

	if project == "" {
		if environment == "" {
			return nil
		}
		return fmt.Errorf("cannot put service %q on environment %q with no project: an environment name is unique per project, not globally, so a rung with no project resolves against nothing. Name the project too (`hz service assign %s <project>/%s`), or clear both (`hz service unassign %s`)",
			service, environment, service, environment, service)
	}

	if !c.hasProject(project) {
		return fmt.Errorf("cannot assign service %q: no project %q is declared, and hz refuses a service naming one that is not — declare it first with `hz project add %s`, then assign%s",
			service, project, project, c.projectHint())
	}

	if environment == "" {
		return nil
	}
	if !c.hasEnvironment(project, environment) {
		return fmt.Errorf("cannot assign service %q to %s/%s: project %q declares no environment %q — declare the rung first with `hz env add %s/%s --posture <dev|staging|prod>`, then assign%s",
			service, project, environment, project, environment, project, environment, c.environmentHint(project))
	}
	return nil
}

// AssignService places one service in the tree, and with both fields empty takes
// it back out.
//
// Whole-value rather than a patch, unlike EnvironmentPatch: the two fields are
// not independent facts. A rung only means anything under a project, so "change
// the environment and leave the project alone" is not an operation an operator
// can reason about — where a service sits is ONE fact with two halves. Clearing
// is therefore expressible without a pointer here: both empty is unassigned.
//
// Returns the service as it now reads, so a caller answers with what hz holds
// rather than an echo of what was asked for.
func (c *Config) AssignService(service, project, environment string) (Service, error) {
	service = strings.TrimSpace(service)
	project = strings.TrimSpace(project)
	environment = strings.TrimSpace(environment)

	idx := -1
	for i, svc := range c.Services {
		if svc.Name == service {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Service{}, fmt.Errorf("no service %q — `hz service list` lists what exists", service)
	}
	if err := c.CheckAssignment(service, project, environment); err != nil {
		return Service{}, err
	}

	next := c.copyForWrite()
	next.Services[idx].Project = project
	next.Services[idx].Environment = environment
	// Belt for the braces above: CheckAssignment answers about this one service,
	// validateModel answers about the whole config, and it is the one Save runs.
	// A writer here must not be able to produce a config Save would later refuse.
	if err := next.validateModel(); err != nil {
		return Service{}, err
	}
	c.adopt(next)
	return next.Services[idx], nil
}
