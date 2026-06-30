package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/templates"
)

// TownCLAUDEmdCheck verifies the town-root CLAUDE.md is up to date with
// the version embedded in the binary. This is the highest-value migration
// check — behavioral norms for agents come from CLAUDE.md.
//
// The town-root CLAUDE.md (~/gt/CLAUDE.md) is loaded by Claude Code for
// all agents running from within the town git tree (Mayor, Deacon).
// It must contain operational norms (Dolt awareness, communication hygiene,
// nudge-first) that guide agent behavior.
type TownCLAUDEmdCheck struct {
	FixableCheck
	missingSections []templates.TownRootRequiredSection
	fileMissing     bool
}

// NewTownCLAUDEmdCheck creates a new town-root CLAUDE.md version check.
func NewTownCLAUDEmdCheck() *TownCLAUDEmdCheck {
	return &TownCLAUDEmdCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "town-claude-md",
				CheckDescription: "Verify town-root CLAUDE.md is up to date with embedded version",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks the town-root CLAUDE.md for completeness.
func (c *TownCLAUDEmdCheck) Run(ctx *CheckContext) *CheckResult {
	c.missingSections = nil
	c.fileMissing = false

	claudePath := filepath.Join(ctx.TownRoot, "CLAUDE.md")

	// Check if file exists
	data, err := os.ReadFile(claudePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.fileMissing = true
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusError,
				Message: "Town-root CLAUDE.md is missing",
				FixHint: "Run 'gt doctor --fix' to create it from embedded template",
			}
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("Cannot read town-root CLAUDE.md: %v", err),
		}
	}

	content := string(data)

	// Check for required sections
	required := templates.TownRootRequiredSections()
	var missing []templates.TownRootRequiredSection
	var details []string

	for _, section := range required {
		if !strings.Contains(content, section.Heading) {
			missing = append(missing, section)
			details = append(details, fmt.Sprintf("Missing: %s (%s)", section.Name, section.Heading))
		}
	}

	if len(missing) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Town-root CLAUDE.md has all required sections",
		}
	}

	c.missingSections = missing

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusWarning,
		Message: fmt.Sprintf("Town-root CLAUDE.md missing %d section(s)", len(missing)),
		Details: details,
		FixHint: "Run 'gt doctor --fix' to add missing sections from embedded template",
	}
}

// Fix updates the town-root CLAUDE.md with missing sections from the
// embedded template while preserving user customizations.
//
// Each missing section is appended with the minimum content needed:
//   - A missing H2 section: append the entire canonical H2 section.
//   - A missing H3 subsection: append only that H3 subsection (not the
//     enclosing H2 section, which may already exist in the user's file).
//
// If both an H2 and an H3 subsection it contains are missing, the H2 is
// appended once (its H3 subsections are skipped to avoid duplication).
func (c *TownCLAUDEmdCheck) Fix(ctx *CheckContext) error {
	claudePath := filepath.Join(ctx.TownRoot, "CLAUDE.md")
	canonical := templates.TownRootCLAUDEmd()

	// If file is missing, create it from the canonical template
	if c.fileMissing {
		return os.WriteFile(claudePath, []byte(canonical), 0644)
	}

	// File exists but is missing sections — append them
	if len(c.missingSections) == 0 {
		return nil
	}

	// Read current content
	data, err := os.ReadFile(claudePath)
	if err != nil {
		return fmt.Errorf("reading CLAUDE.md: %w", err)
	}
	current := string(data)

	// Parse canonical content into H2 sections
	canonicalSections := parseH2Sections(canonical)

	// Track which canonical H2 sections we've already appended in full,
	// and which H3 headings were covered by such an H2 append (so we
	// don't append the same H3 twice).
	appendedH2 := make(map[string]bool)
	coveredH3 := make(map[string]bool)

	var toAppend strings.Builder
	for _, missing := range c.missingSections {
		if isH2Heading(missing.Heading) {
			// Append the full canonical H2 section whose heading
			// starts with the missing H2 heading text.
			for _, cs := range canonicalSections {
				if appendedH2[cs.heading] {
					continue
				}
				if strings.HasPrefix(cs.heading, missing.Heading) {
					toAppend.WriteString("\n")
					toAppend.WriteString(cs.content)
					appendedH2[cs.heading] = true
					// Mark all H3 subsections as covered.
					for _, line := range strings.Split(cs.content, "\n") {
						if strings.HasPrefix(line, "### ") {
							coveredH3[line] = true
						}
					}
					break
				}
			}
			continue
		}

		// H3 (or deeper) — append only the missing subsection.
		if coveredH3[missing.Heading] {
			continue
		}
		for _, cs := range canonicalSections {
			h3 := extractH3FromH2(cs.content, missing.Heading)
			if h3 != "" {
				toAppend.WriteString("\n")
				toAppend.WriteString(h3)
				break
			}
		}
	}

	if toAppend.Len() == 0 {
		return nil
	}

	// Ensure current content ends with a newline before appending
	if !strings.HasSuffix(current, "\n") {
		current += "\n"
	}

	updated := current + toAppend.String()
	return os.WriteFile(claudePath, []byte(updated), 0644)
}

// isH2Heading reports whether heading is an H2 markdown heading
// (starts with "## " but not "### ").
func isH2Heading(heading string) bool {
	return strings.HasPrefix(heading, "## ") && !strings.HasPrefix(heading, "### ")
}

// extractH3FromH2 extracts a single H3 subsection (heading line plus body
// up to the next H2 or H3 boundary) from an H2 section body. Returns the
// empty string if h3Heading is not found.
func extractH3FromH2(h2Body, h3Heading string) string {
	lines := strings.Split(h2Body, "\n")
	var sb strings.Builder
	inSection := false
	for _, line := range lines {
		if strings.HasPrefix(line, "### ") ||
			(strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "### ")) {
			if inSection {
				break
			}
			if line == h3Heading {
				inSection = true
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		} else if inSection {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	if !strings.HasSuffix(sb.String(), "\n") {
		sb.WriteString("\n")
	}
	return sb.String()
}

// h2Section represents a section of markdown delimited by H2 headings.
type h2Section struct {
	heading string // The H2 heading line (e.g., "## Dolt Server — Operational Awareness")
	content string // Full section content including the heading and all sub-content
}

// parseH2Sections splits markdown content into sections by H2 headings.
// The preamble (content before the first H2) is returned as a section with
// an empty heading.
func parseH2Sections(content string) []h2Section {
	var sections []h2Section
	lines := strings.Split(content, "\n")

	var currentHeading string
	var currentContent strings.Builder
	inSection := false

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			// Save previous section
			if inSection || currentContent.Len() > 0 {
				sections = append(sections, h2Section{
					heading: currentHeading,
					content: currentContent.String(),
				})
			}
			// Start new section
			currentHeading = line
			currentContent.Reset()
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			inSection = true
		} else {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	// Save final section
	if currentContent.Len() > 0 {
		sections = append(sections, h2Section{
			heading: currentHeading,
			content: currentContent.String(),
		})
	}

	return sections
}
