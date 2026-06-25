package cmd

import (
	"fmt"
	"strings"
)

// formulaTargetRole maps a formula name to the agent role it is intended for.
// A bead whose body points to a different role should not be dispatched under
// that formula (gt-c76 / gastownhall/gastown#3852).
var formulaTargetRole = map[string]string{
	"mol-polecat-work": "polecat",
}

// roleContentIndicators maps agent roles to content signals that strongly
// suggest a bead belongs to that role rather than the target formula's role.
// All matching is case-insensitive.
var roleContentIndicators = map[string][]string{
	"deacon": {
		"gt deacon",
		"--agent-bead hq-deacon",
		"deacon session",
		"deacon patrol",
	},
}

// orderedRoles controls deterministic iteration over roleContentIndicators.
// Add new guarded roles here so checks run in a stable order.
var orderedRoles = []string{"deacon"}

// ErrAgentRoleMismatch indicates that a bead's content points to a different
// agent role than the formula under which it is being scheduled/dispatched.
var ErrAgentRoleMismatch = fmt.Errorf("bead content does not match target formula role")

// detectConflictingRole scans title and description for content indicators of
// an agent role other than targetRole. It returns the detected role and the
// signal substring that matched, or ("", "", false) if no conflict is found.
func detectConflictingRole(title, description, targetRole string) (role, signal string, found bool) {
	if targetRole == "" {
		return "", "", false
	}

	text := strings.ToLower(title) + "\n" + strings.ToLower(description)

	for _, role := range orderedRoles {
		if role == targetRole {
			continue
		}
		for _, ind := range roleContentIndicators[role] {
			if strings.Contains(text, strings.ToLower(ind)) {
				return role, ind, true
			}
		}
	}

	return "", "", false
}

// ValidateBeadContentForFormula checks whether a bead's title and description
// are compatible with the agent role that targetFormula is designed for. It
// returns ErrAgentRoleMismatch when the bead content clearly references a
// different agent role.
//
// The check is intentionally conservative: only well-known formula/role
// combinations and strong role-specific signals are considered, so ordinary
// code tasks are not rejected.
func ValidateBeadContentForFormula(title, description, targetFormula string) error {
	targetRole, known := formulaTargetRole[targetFormula]
	if !known {
		return nil
	}

	if role, signal, found := detectConflictingRole(title, description, targetRole); found {
		return fmt.Errorf("%w: formula %q targets role %q but bead references %s (signal: %q)",
			ErrAgentRoleMismatch, targetFormula, targetRole, role, signal)
	}

	return nil
}
