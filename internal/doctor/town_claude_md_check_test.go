package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/templates"
)

func TestTownCLAUDEmdCheck_Missing(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusError {
		t.Errorf("expected StatusError for missing file, got %v", result.Status)
	}
	if !check.fileMissing {
		t.Error("expected fileMissing=true")
	}
}

func TestTownCLAUDEmdCheck_Complete(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write the canonical content
	canonical := templates.TownRootCLAUDEmd()
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(canonical), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("expected StatusOK for complete file, got %v: %s", result.Status, result.Message)
	}
}

func TestTownCLAUDEmdCheck_MissingSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write only the identity anchor (no Dolt or communication sections)
	content := `# Gas Town

This is a Gas Town workspace. Your identity and role are determined by ` + "`gt prime`" + `.

Run ` + "`gt prime`" + ` for full context after compaction, clear, or new session.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning for missing sections, got %v", result.Status)
	}
	if len(check.missingSections) != 2 {
		t.Errorf("expected 2 missing sections, got %d", len(check.missingSections))
	}
}

func TestTownCLAUDEmdCheck_PartialSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write identity anchor + Dolt section but no communication hygiene
	content := `# Gas Town

This is a Gas Town workspace.

## Dolt Server — Operational Awareness

Dolt is the data plane for beads.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", result.Status)
	}
	if len(check.missingSections) != 1 {
		t.Errorf("expected 1 missing section, got %d", len(check.missingSections))
	}
	if check.missingSections[0].Name != "Communication hygiene" {
		t.Errorf("expected 'Communication hygiene' missing, got %q", check.missingSections[0].Name)
	}
}

func TestTownCLAUDEmdCheck_Fix_MissingFile(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("expected StatusError, got %v", result.Status)
	}

	// Fix should create the file from canonical
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify file was created
	data, err := os.ReadFile(filepath.Join(tmpDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "## Dolt Server") {
		t.Error("created file missing Dolt Server section")
	}
	if !strings.Contains(content, "### Communication hygiene") {
		t.Error("created file missing Communication hygiene section")
	}
}

func TestTownCLAUDEmdCheck_Fix_AppendSections(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write minimal anchor + a user custom section
	original := `# Gas Town

This is a Gas Town workspace.

## My Custom Section

This is user-added content that should be preserved.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v", result.Status)
	}

	// Fix should append missing sections
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify file was updated
	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// User's custom section should be preserved
	if !strings.Contains(content, "## My Custom Section") {
		t.Error("user custom section was not preserved")
	}
	if !strings.Contains(content, "user-added content") {
		t.Error("user custom content was not preserved")
	}

	// Missing sections should be appended
	if !strings.Contains(content, "## Dolt Server") {
		t.Error("Dolt Server section was not appended")
	}
	if !strings.Contains(content, "### Communication hygiene") {
		t.Error("Communication hygiene section was not appended")
	}
}

func TestTownCLAUDEmdCheck_Fix_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Write the canonical content
	canonical := templates.TownRootCLAUDEmd()
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(canonical), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK, got %v", result.Status)
	}

	// Fix on an OK file should be a no-op
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != canonical {
		t.Error("fix modified a complete file (should be idempotent)")
	}
}

// TestTownCLAUDEmdCheck_Fix_NoH2Duplication exercises the retro-bug P0 case:
// when both an H2 section ("## Dolt Server") and an H3 subsection it contains
// ("### Communication hygiene") are missing, Fix must append the H2 section
// exactly once — not once for the H2 and a second time for the H3 (the
// pre-fix behavior). The check matches the canonical "## Dolt Server —
// Operational Awareness (All Agents)" heading as the H2 to append.
func TestTownCLAUDEmdCheck_Fix_NoH2Duplication(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// Start from a minimal file that has neither required section.
	original := `# Gas Town

This is a Gas Town workspace.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v", result.Status)
	}
	if len(check.missingSections) != 2 {
		t.Fatalf("expected 2 missing sections (H2 + H3), got %d", len(check.missingSections))
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// The H2 header line must appear exactly once.
	const h2 = "## Dolt Server — Operational Awareness (All Agents)"
	if c := strings.Count(content, h2); c != 1 {
		t.Errorf("expected H2 %q to appear exactly once, got %d occurrences", h2, c)
	}

	// And the H3 subsection must appear exactly once (inside the appended H2).
	const h3 = "### Communication hygiene"
	if c := strings.Count(content, h3); c != 1 {
		t.Errorf("expected H3 %q to appear exactly once, got %d occurrences", h3, c)
	}
}

// TestTownCLAUDEmdCheck_Fix_H3OnlyAppendsSubsection covers the case where the
// user already has the "## Dolt Server" H2 but is missing the "###
// Communication hygiene" H3 subsection. Fix must append ONLY the missing H3
// subsection — not the entire enclosing H2 (which would duplicate the header
// and re-add every other H3 subsection).
func TestTownCLAUDEmdCheck_Fix_H3OnlyAppendsSubsection(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &CheckContext{TownRoot: tmpDir}

	// User has the H2 but no H3 subsection — use a unique heading so Run only
	// flags the H3 as missing.
	original := `# Gas Town

This is a Gas Town workspace.

## Dolt Server — Operational Awareness (All Agents)

User-maintained Dolt content here.
`
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewTownCLAUDEmdCheck()
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v", result.Status)
	}
	if len(check.missingSections) != 1 || check.missingSections[0].Name != "Communication hygiene" {
		t.Fatalf("expected only 'Communication hygiene' missing, got %v", check.missingSections)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(claudePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// H3 was appended.
	if !strings.Contains(content, "### Communication hygiene") {
		t.Error("expected H3 'Communication hygiene' to be appended")
	}

	// H2 header must still appear exactly once — must not be duplicated.
	const h2 = "## Dolt Server — Operational Awareness (All Agents)"
	if c := strings.Count(content, h2); c != 1 {
		t.Errorf("expected H2 %q to appear exactly once, got %d occurrences", h2, c)
	}

	// User content must be preserved.
	if !strings.Contains(content, "User-maintained Dolt content here.") {
		t.Error("user content was not preserved")
	}
}

// TestExtractH3FromH2 verifies the H3 extractor returns only the matched
// subsection (heading + body up to the next H2/H3), not the entire H2 body.
func TestExtractH3FromH2(t *testing.T) {
	body := `## Dolt Server

Intro paragraph that should not appear in extraction.

### Subsection A

Content A.

### Subsection B

Content B line 1.
Content B line 2.

## Next Section

Should not appear.
`

	got := extractH3FromH2(body, "### Subsection B")
	want := "### Subsection B\n\nContent B line 1.\nContent B line 2.\n\n"
	if got != want {
		t.Errorf("extractH3FromH2 mismatch\n got: %q\nwant: %q", got, want)
	}

	if got := extractH3FromH2(body, "### Does Not Exist"); got != "" {
		t.Errorf("expected empty string for missing H3, got %q", got)
	}
}

func TestParseH2Sections(t *testing.T) {
	content := `# Header

Preamble text.

## Section One

Content one.

## Section Two

Content two.
### Subsection

Sub content.

## Section Three

Content three.
`

	sections := parseH2Sections(content)

	if len(sections) != 4 { // preamble + 3 H2 sections
		t.Fatalf("expected 4 sections, got %d", len(sections))
	}

	// Preamble
	if sections[0].heading != "" {
		t.Errorf("preamble should have empty heading, got %q", sections[0].heading)
	}
	if !strings.Contains(sections[0].content, "Preamble text") {
		t.Error("preamble missing expected content")
	}

	// Section One
	if sections[1].heading != "## Section One" {
		t.Errorf("expected '## Section One', got %q", sections[1].heading)
	}

	// Section Two (should include H3 subsection)
	if sections[2].heading != "## Section Two" {
		t.Errorf("expected '## Section Two', got %q", sections[2].heading)
	}
	if !strings.Contains(sections[2].content, "### Subsection") {
		t.Error("Section Two should include H3 subsection")
	}

	// Section Three
	if sections[3].heading != "## Section Three" {
		t.Errorf("expected '## Section Three', got %q", sections[3].heading)
	}
}

func TestIsIdentityAnchor_MinimalAnchor(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	content := `# Gas Town

Run ` + "`gt prime`" + ` for full context.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if !isIdentityAnchor(path) {
		t.Error("minimal anchor should be recognized as identity anchor")
	}
}

func TestIsIdentityAnchor_ExpandedCLAUDEmd(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	// Write canonical content (many lines)
	if err := os.WriteFile(path, []byte(templates.TownRootCLAUDEmd()), 0644); err != nil {
		t.Fatal(err)
	}

	if !isIdentityAnchor(path) {
		t.Error("expanded CLAUDE.md should be recognized as identity anchor")
	}
}

func TestIsIdentityAnchor_NonGasTownFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "CLAUDE.md")

	content := `# My Project

This is a regular project CLAUDE.md, not Gas Town.
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if isIdentityAnchor(path) {
		t.Error("non-Gas Town file should not be recognized as identity anchor")
	}
}
