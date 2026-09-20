package flywheel

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// workerAdapters are the adapters a worker may use; a staffed role may also
// be "cli", a person at the terminal.
var workerAdapters = []string{"opencode", "sim", "claude", "codex"}

// quoted renders names as a comma-ready list of Go string literals:
// [opencode sim] becomes ["opencode" "sim"].
func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strconv.Quote(n)
	}
	return out
}

// adapterKnown reports whether name is an adapter flywheel dispatches to,
// or "cli" when cli is true (a role a person holds).
func adapterKnown(name string, cli bool) bool {
	if cli && name == "cli" {
		return true
	}
	for _, a := range workerAdapters {
		if a == name {
			return true
		}
	}
	return false
}

// roles returns the config's three roles in summary order, including the
// ones it does not name (a nil RoleConfig).
func (s *StaffingConfig) roles() []struct {
	Name string
	Cfg  *RoleConfig
} {
	out := []struct {
		Name string
		Cfg  *RoleConfig
	}{{"lead", nil}, {"inspector", nil}, {"auditor", nil}}
	if s != nil {
		out[0].Cfg, out[1].Cfg, out[2].Cfg = s.Lead, s.Inspector, s.Auditor
	}
	return out
}

// StaffingLine renders one configured role for the factory summary:
// "lead: claude claude-opus-5 (session lead-fw)", with the parts that are
// set; "" when the role is nil or empty.
func StaffingLine(role string, r *RoleConfig) string {
	if r == nil {
		return ""
	}
	if r.Adapter == "" && r.Model == "" && r.Session == "" {
		return ""
	}
	var parts []string
	if r.Adapter != "" {
		parts = append(parts, r.Adapter)
	}
	if r.Model != "" {
		parts = append(parts, r.Model)
	}
	line := strings.Join(parts, " ")
	if line == "" {
		return ""
	}
	if r.Session != "" {
		line = fmt.Sprintf("%s (session %s)", line, r.Session)
	}
	return fmt.Sprintf("%s: %s", role, line)
}

// StaffingMismatch reports the roles whose registered staffed event does not
// match the configured role (issue #69): for each of lead, inspector and
// auditor that the config names with a session, the latest staffed event of
// that role must carry the same session, else a line
// "lead: config says lead-fw, floor says lead-other". Roles the config does
// not name, or that were never staffed, are skipped. The result is sorted by
// role name (auditor, inspector, lead).
func StaffingMismatch(cfg Config, events []Event) []string {
	if cfg.Staffing == nil {
		return nil
	}
	staffed := map[string]staffRole{}
	for _, e := range events {
		if e.Kind == "staffed" {
			staffed[e.Persona] = staffRole{Session: e.Session, Model: e.Model}
		}
	}
	var mismatches []string
	for _, role := range cfg.Staffing.roles() {
		if role.Cfg == nil || role.Cfg.Session == "" {
			continue
		}
		if floor, ok := staffed[role.Name]; ok && floor.Session != role.Cfg.Session {
			mismatches = append(mismatches, fmt.Sprintf("%s: config says %s, floor says %s", role.Name, role.Cfg.Session, floor.Session))
		}
	}
	sort.Strings(mismatches)
	return mismatches
}
