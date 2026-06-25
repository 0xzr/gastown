package cmd

import (
	"github.com/spf13/cobra"
)

// consult command flags.
var (
	consultTrigger     string
	consultModel       string
	consultRelatedBead string
	consultFingerprint string
	consultContextRefs []string
	consultOptions     []string
	consultCurrent     string
	consultAskedBy     string
	consultStdin       bool
	consultReason      string
	consultDecisionBy  string
	consultDecision    string
	consultRationale   string
	consultConfidence  string
	consultListJSON    bool
	consultListAll     bool
	consultShowJSON    bool
	consultCloseJSON   bool
	consultDryRun      bool
)

var consultCmd = &cobra.Command{
	Use:     "consult [question]",
	Aliases: []string{"ask"},
	GroupID: GroupComm,
	Short:   "File a durable consult packet for Codex or Opus",
	RunE:    runConsult,
	Long: `File a durable consult packet asking a stronger model (Codex or Opus) to
weigh in on a high-risk Mayor decision.

The Mayor dispatches most work itself. When a decision crosses one of the
five trigger classes, the Mayor must consult instead of guessing:

  merge_policy              - changes to town-wide merge / refinery rules
  witness_refinery_override - manual Witness or Refinery override
  recovery_loop             - same failure fingerprint keeps firing
  ambiguous_directive       - operator message the Mayor cannot parse
  low_confidence_output     - Mayor itself judges its own output unreliable

The consult packet is a bead labeled gt:consult with structured fields
(question, trigger class, requested model, options, current decision,
context refs, related bead, fingerprint). It is durable: a follow-up
session can pick it up via gt consult show <id>.

Routing defaults to gt mail to the configured consult target (Codex or
Opus). The packet is NOT routed to email/SMS/Slack by default; this keeps
the lightweight-dispatch promise intact while still producing a permanent
record.

WORKFLOW:
  1. Mayor:    gt consult "Should we allow stacked-branch tip-only MRs?"
               --trigger merge_policy --model opus --related gastown-xyz
  2. Operator: reviews the bead, drafts a response, runs:
               gt consult answer <id> --decided-by opus --decision "..."
               --rationale "..." [--confidence high]
  3. Mayor:    gt consult close <id> --reason "merged; rationale recorded"
               (this also mirrors the decision onto the source bead)

CONFIGURATION:
  Routing targets live in ~/gt/settings/consult.json:
    - targets.codex      (default: "codex")
    - targets.opus       (default: "opus")
    - loop.threshold     (default: 3 occurrences in 30m => consult)
    - loop.escalate_at   (default: 6 occurrences in 30m => escalate)

Examples:
  gt consult "Allow stacked-branch tip-only MRs?" --trigger merge_policy --model opus --related gastown-cet.2.3
  gt consult list
  gt consult show <id>
  gt consult answer <id> --decided-by opus --decision "Allow with squash" --rationale "..."
  gt consult close <id> --reason "acted on consultation"`,
}

var consultListCmd = &cobra.Command{
	Use:   "list",
	Short: "List open consult packets",
	Long: `List all open consult packets.

By default shows only open packets. Use --all to include closed ones.
Use --json for machine-readable output.`,
	RunE: runConsultList,
}

var consultShowCmd = &cobra.Command{
	Use:   "show <consult-id>",
	Short: "Show details of a consult packet",
	Long: `Display detailed information about a consult packet including the
question, trigger class, options, current decision, and (if answered) the
consulted model's response.`,
	Args: cobra.ExactArgs(1),
	RunE: runConsultShow,
}

var consultAnswerCmd = &cobra.Command{
	Use:   "answer <consult-id>",
	Short: "Record the consulted model's response",
	Long: `Record the consulted model's decision and rationale on the consult
bead. This stamps the bead with answered_by / answered_at / decision /
rationale / confidence fields but does NOT close it. Use 'gt consult close'
when the Mayor has acted on the response.`,
	Args: cobra.ExactArgs(1),
	RunE: runConsultAnswer,
}

var consultCloseCmd = &cobra.Command{
	Use:   "close <consult-id>",
	Short: "Close a consult packet and mirror the decision onto the source bead",
	Long: `Close a consult packet. When the packet has a recorded response,
the decision is mirrored onto the related bead via 'bd update --notes'
so the source bead carries the consulted model's decision in its own
audit trail. Use --reason to record why the packet is being closed.`,
	Args: cobra.ExactArgs(1),
	RunE: runConsultClose,
}

func init() {
	consultCmd.Flags().StringVar(&consultTrigger, "trigger", "", "Trigger class: merge_policy | witness_refinery_override | recovery_loop | ambiguous_directive | low_confidence_output")
	consultCmd.Flags().StringVar(&consultModel, "model", "", "Requested model: codex | opus")
	consultCmd.Flags().StringVar(&consultRelatedBead, "related", "", "Source bead ID this consult is about")
	consultCmd.Flags().StringVar(&consultFingerprint, "fingerprint", "", "Stable duplicate-suppression key")
	consultCmd.Flags().StringSliceVar(&consultContextRefs, "context", nil, "Bead or MR IDs the consult receiver should read first (repeatable)")
	consultCmd.Flags().StringSliceVar(&consultOptions, "option", nil, "Option the Mayor is considering (repeatable)")
	consultCmd.Flags().StringVar(&consultCurrent, "current", "", "Mayor's current best-guess decision")
	consultCmd.Flags().StringVar(&consultAskedBy, "asked-by", "", "Mayor actor identity (defaults to env / git user)")
	consultCmd.Flags().StringVar(&consultReason, "reason", "", "Reason / context for the consult (used as question fallback when no question is given)")
	consultCmd.Flags().BoolVar(&consultStdin, "stdin", false, "Read the question from stdin (avoids shell quoting issues)")
	consultCmd.Flags().BoolVar(&consultDryRun, "dry-run", false, "Show what would be filed without creating a bead or sending mail")
	consultCmd.Flags().BoolVar(&consultListJSON, "json", false, "Output as JSON")

	consultListCmd.Flags().BoolVar(&consultListJSON, "json", false, "Output as JSON")
	consultListCmd.Flags().BoolVar(&consultListAll, "all", false, "Include closed consult packets")

	consultShowCmd.Flags().BoolVar(&consultShowJSON, "json", false, "Output as JSON")

	consultAnswerCmd.Flags().StringVar(&consultDecisionBy, "decided-by", "", "Consulted model identifier (required)")
	consultAnswerCmd.Flags().StringVar(&consultDecision, "decision", "", "Decision (chosen option verbatim or new option) (required)")
	consultAnswerCmd.Flags().StringVar(&consultRationale, "rationale", "", "Consulted model's rationale (required)")
	consultAnswerCmd.Flags().StringVar(&consultConfidence, "confidence", "", "Self-reported confidence: high | medium | low")
	consultAnswerCmd.Flags().BoolVar(&consultDryRun, "dry-run", false, "Show what would be recorded without updating the bead")
	consultAnswerCmd.Flags().BoolVar(&consultCloseJSON, "json", false, "Output as JSON")

	consultCloseCmd.Flags().StringVar(&consultReason, "reason", "", "Reason the packet is being closed (required)")
	consultCloseCmd.Flags().BoolVar(&consultDryRun, "dry-run", false, "Show what would be closed/mirrored without acting")
	consultCloseCmd.Flags().BoolVar(&consultCloseJSON, "json", false, "Output as JSON")

	consultCmd.AddCommand(consultListCmd)
	consultCmd.AddCommand(consultShowCmd)
	consultCmd.AddCommand(consultAnswerCmd)
	consultCmd.AddCommand(consultCloseCmd)

	rootCmd.AddCommand(consultCmd)
}
