package config

import (
	"fmt"
	"strings"
)

// PinnedLine keeps a release line supported on every rung of a project beyond
// the two hz derives (the declared version's line and the one promoted before
// it — db.DeriveSupportedLines). A supported line needs a kept backup and a
// passing restore test before anything is promoted past it, so a pin is a
// standing cost, and a cost nobody is obliged to explain is one nobody
// explains: a pin without a Reason is refused, as a multi-homed machine
// without a Note is (CLAUDE.md #10).
//
// This package does not parse Line. hz's one version parser is in internal/db,
// and importing it here would link SQLite into hz-agent, which links config.
// The pin writer (POST /api/v1/projects/lines/pin) checks the spelling with
// db.CheckLine; a hand-edited pin that is not a line reaches the derivation as
// a GAP, which the promotion gate refuses — unknown, never silently empty.
type PinnedLine struct {
	Line   string `json:"line"`
	Reason string `json:"reason"`
}

func (p Project) validatePins() error {
	seen := make(map[string]bool, len(p.PinnedLines))
	for _, pin := range p.PinnedLines {
		if strings.TrimSpace(pin.Line) == "" {
			return fmt.Errorf("project %q pins a line with no name", p.Name)
		}
		if strings.TrimSpace(pin.Reason) == "" {
			return fmt.Errorf("project %q pins line %s without a reason — a pin keeps a line supported, and every "+
				"supported line needs a kept backup and a restore test; say why (--reason)", p.Name, pin.Line)
		}
		if seen[pin.Line] {
			return fmt.Errorf("project %q pins line %s twice", p.Name, pin.Line)
		}
		seen[pin.Line] = true
	}
	return nil
}

// PinLine pins a line on a project, or replaces the reason of an existing pin.
// The slice is copied: a Config is shared by shallow copy (`next := *s.cfg()`),
// and writing through the old backing array would change the live one.
func (c *Config) PinLine(project, line, reason string) error {
	for i := range c.Projects {
		if c.Projects[i].Name != project {
			continue
		}
		projects := append([]Project(nil), c.Projects...)
		pins := append([]PinnedLine(nil), projects[i].PinnedLines...)
		replaced := false
		for j := range pins {
			if pins[j].Line == line {
				pins[j].Reason = reason
				replaced = true
			}
		}
		if !replaced {
			pins = append(pins, PinnedLine{Line: line, Reason: reason})
		}
		projects[i].PinnedLines = pins
		if err := projects[i].validatePins(); err != nil {
			return err
		}
		c.Projects = projects
		return nil
	}
	return fmt.Errorf("no project %q — `hz project ls` lists what exists", project)
}

// UnpinLine removes a pin. Unpinning a line that is not pinned is an error, not
// a no-op: the caller meant some line, and it is not this one.
func (c *Config) UnpinLine(project, line string) error {
	for i := range c.Projects {
		if c.Projects[i].Name != project {
			continue
		}
		pins := make([]PinnedLine, 0, len(c.Projects[i].PinnedLines))
		for _, pin := range c.Projects[i].PinnedLines {
			if pin.Line != line {
				pins = append(pins, pin)
			}
		}
		if len(pins) == len(c.Projects[i].PinnedLines) {
			return fmt.Errorf("project %q does not pin line %s", project, line)
		}
		if len(pins) == 0 {
			pins = nil
		}
		projects := append([]Project(nil), c.Projects...)
		projects[i].PinnedLines = pins
		c.Projects = projects
		return nil
	}
	return fmt.Errorf("no project %q — `hz project ls` lists what exists", project)
}

// ProjectPins is a project's pins, or nil for a project that pins none or
// does not exist.
func (c *Config) ProjectPins(project string) []PinnedLine {
	for _, p := range c.Projects {
		if p.Name == project {
			return p.PinnedLines
		}
	}
	return nil
}
