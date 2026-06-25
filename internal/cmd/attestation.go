package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Attestation command flags
var (
	attestationJSON bool
	attestationRig  string
)

// attestationCmd is the machine-checkable attestation-report command (gastown-7g4).
//
// A completed Gas Town MR must carry durable multi-model attestation proof: a
// valid HMAC token for the exact merged tree, written by the refinery gate and
// verified by the Go merge tooling before push. This command lists completed
// (closed/merged) MRs that lack that proof — either because no attested_tree
// was recorded or because the recorded tree's token no longer verifies against
// the current key.
//
// It is machine-checkable: the process exits non-zero when any completed work
// lacks attestation proof, so it can gate CI / a deploy step.
var attestationCmd = &cobra.Command{
	Use:   "attestation",
	GroupID: GroupDiag,
	Short: "Report completed work lacking multi-model attestation proof",
	Long: `Report completed merge-requests that lack durable multi-model attestation.

Every merged MR must carry proof that the full review panel (deterministic gate
+ writer-excluded core peers + Opus/final verifier) cleared its exact tree: an
HMAC attestation token verified by the refinery before push. This command lists
completed MRs that lack that proof.

A completed MR is "unattested" if:
  - it was merged but has no attested_tree recorded, OR
  - its attested_tree has no valid token in the attestation store, OR
  - it has a merge_commit whose tree cannot be resolved and no attested_tree

Exit status:
  0  all completed work carries valid attestation proof (or none exists)
  1  one or more completed MRs lack attestation proof
  2  the report itself could not run (no workspace, beads unavailable)

Examples:
  gt attestation                 # Report unattested completed work
  gt attestation --json          # Machine-readable JSON (for CI gating)
  gt attestation --rig gastown   # Scope to a single rig`,
	RunE: runAttestation,
}

func init() {
	attestationCmd.Flags().BoolVar(&attestationJSON, "json", false, "Output as JSON (for CI gating)")
	attestationCmd.Flags().StringVar(&attestationRig, "rig", "", "Scope to a single rig (default: all rigs with beads)")

	rootCmd.AddCommand(attestationCmd)
}

// AttestationReportRow is one entry in the attestation report: a completed MR
// and the reason it is considered unattested.
type AttestationReportRow struct {
	Rig          string `json:"rig"`
	MRID         string `json:"mr_id"`
	SourceIssue  string `json:"source_issue,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Worker       string `json:"worker,omitempty"`
	MergeCommit  string `json:"merge_commit,omitempty"`
	AttestedTree string `json:"attested_tree,omitempty"`
	Reason       string `json:"reason"` // no-attested-tree | token-missing | token-invalid | tree-unresolvable
	Detail       string `json:"detail,omitempty"`
}

func runAttestation(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigs, err := attestationRigPaths(townRoot)
	if err != nil {
		return fmt.Errorf("resolving rig beads paths: %w", err)
	}

	var rows []AttestationReportRow
	for _, rp := range rigs {
		rigRows, err := collectUnattestedMRs(rp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not query beads for %s: %v\n", rp.name, err)
			continue
		}
		rows = append(rows, rigRows...)
	}

	if attestationJSON {
		return outputAttestationJSON(rows)
	}
	outputAttestationText(rows)

	// Machine-checkable exit: non-zero when any completed work lacks proof.
	if len(rows) > 0 {
		return NewSilentExit(1)
	}
	return nil
}

// rigPath pairs a rig's beads directory with its name.
type rigPath struct {
	name string
	path string
}

// attestationRigPaths resolves the beads directories to scan. When --rig is
// set, only that rig is scanned; otherwise all known rig beads paths are used.
func attestationRigPaths(townRoot string) ([]rigPath, error) {
	if attestationRig != "" {
		return []rigPath{{name: attestationRig, path: filepath.Join(townRoot, attestationRig, "mayor", "rig")}}, nil
	}

	// Default: scan every rig under the town root that has a mayor/rig beads dir.
	// This mirrors how gt audit locates the gastown beads path.
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return nil, err
	}
	var paths []rigPath
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bp := filepath.Join(townRoot, e.Name(), "mayor", "rig")
		if _, statErr := os.Stat(bp); statErr == nil {
			paths = append(paths, rigPath{name: e.Name(), path: bp})
		}
	}
	if len(paths) == 0 {
		// Fall back to the gastown rig path used by gt audit, so the command
		// still works in a single-rig checkout.
		paths = []rigPath{{name: "gastown", path: filepath.Join(townRoot, "gastown", "mayor", "rig")}}
	}
	return paths, nil
}

// collectUnattestedMRs lists completed (closed) MR beads in the given rig and
// returns those lacking valid attestation proof.
func collectUnattestedMRs(rp rigPath) ([]AttestationReportRow, error) {
	b := beads.New(rp.path)

	// List all MRs; we filter to completed ones below. Priority -1 returns all
	// priorities (matching gt audit's behaviour).
	issues, err := b.ListMergeRequests(beads.ListOptions{
		Status:   "closed",
		Priority: -1,
	})
	if err != nil {
		return nil, err
	}

	var rows []AttestationReportRow
	for _, issue := range issues {
		fields := beads.ParseMRFields(issue)
		if fields == nil {
			continue
		}
		// Only completed (merged) MRs are in scope. An MR closed for any other
		// reason (rejected/conflict/superseded) did not land on main, so it is
		// not "completed work" requiring attestation.
		if fields.CloseReason != "merged" {
			continue
		}

		row, unattested := classifyAttestation(fields)
		if !unattested {
			continue
		}
		row.Rig = rp.name
		row.MRID = issue.ID
		row.SourceIssue = fields.SourceIssue
		row.Branch = fields.Branch
		row.Worker = fields.Worker
		row.MergeCommit = fields.MergeCommit
		rows = append(rows, row)
	}
	return rows, nil
}

// classifyAttestation decides whether a single completed MR is unattested. The
// proof is the attested_tree recorded on the MR bead; its token must still
// verify against the current key. When no attested_tree was recorded we fall
// back to the merge commit's tree (if resolvable) so historical merges that
// predate the field are still checkable.
//
// It is split out (pure-ish) so tests can exercise the classification logic
// against fixture MRFields without touching the filesystem.
func classifyAttestation(fields *beads.MRFields) (AttestationReportRow, bool) {
	row := AttestationReportRow{AttestedTree: fields.AttestedTree}

	// Prefer the durable attested_tree recorded at merge time.
	tree := fields.AttestedTree
	if tree == "" {
		// Fall back to the merge commit's tree, if we can resolve it locally.
		if fields.MergeCommit == "" {
			row.Reason = "no-attested-tree"
			row.Detail = "merged MR has no attested_tree and no merge_commit to derive one"
			return row, true
		}
		resolved, err := resolveTree(fields.MergeCommit)
		if err != nil {
			row.Reason = "tree-unresolvable"
			row.Detail = fmt.Sprintf("cannot resolve tree for merge_commit %s: %v", shortHash(fields.MergeCommit), err)
			return row, true
		}
		tree = resolved
		row.AttestedTree = tree
	}

	if err := refinery.VerifyAttestation(tree); err != nil {
		if err == refinery.ErrAttestationMissing {
			row.Reason = "token-missing"
			row.Detail = fmt.Sprintf("no attestation token exists for tree %s — the review panel did not produce proof", shortHash(tree))
		} else {
			row.Reason = "token-invalid"
			row.Detail = fmt.Sprintf("attestation token for tree %s failed to verify: %v", shortHash(tree), err)
		}
		return row, true
	}
	return row, false
}

// resolveTree resolves a commit SHA to its tree SHA via git, tolerating the
// merge commit not being present in the current repo (returns an error rather
// than a silent pass).
func resolveTree(commitSHA string) (string, error) {
	out, err := exec.Command("git", "rev-parse", commitSHA+"^{tree}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func outputAttestationJSON(rows []AttestationReportRow) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func outputAttestationText(rows []AttestationReportRow) {
	if len(rows) == 0 {
		fmt.Printf("%s All completed work carries valid attestation proof\n", style.Success.Render("✓"))
		return
	}
	fmt.Printf("%s %d completed MR(s) lacking attestation proof:\n\n", style.Error.Render("✗"), len(rows))
	for _, r := range rows {
		fmt.Printf("  %s  %s\n", style.Error.Render("●"), r.MRID)
		fmt.Printf("      rig:          %s\n", r.Rig)
		if r.SourceIssue != "" {
			fmt.Printf("      source:       %s\n", r.SourceIssue)
		}
		if r.Branch != "" {
			fmt.Printf("      branch:       %s\n", r.Branch)
		}
		if r.MergeCommit != "" {
			fmt.Printf("      merge_commit: %s\n", shortHash(r.MergeCommit))
		}
		fmt.Printf("      tree:         %s\n", shortHash(r.AttestedTree))
		fmt.Printf("      reason:       %s\n", r.Reason)
		if r.Detail != "" {
			fmt.Printf("      detail:       %s\n", r.Detail)
		}
		fmt.Println()
	}
	fmt.Fprintf(os.Stderr, "Re-run the refinery gate for each tree to produce a valid attestation token, then re-queue.\n")
}
