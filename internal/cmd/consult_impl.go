package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/consult"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// consultDefaultTargets maps each requested model to a default mail target.
// Operators can override per town via settings/consult.json (see
// docs/mayor-consult-policy.md). The defaults are intentionally conservative:
// the consult packet is durable even if the mail target is unreachable.
var consultDefaultTargets = map[consult.RequestedModel]string{
	consult.ModelCodex: "codex",
	consult.ModelOpus:  "opus",
}

func consultActor() string {
	if v := os.Getenv("BD_ACTOR"); v != "" {
		return v
	}
	if v := os.Getenv("GT_ROLE"); v != "" {
		return v
	}
	return "mayor"
}

// consultConfigDefaults returns the per-town consult configuration. For now
// the only knob is the mail target per model; loop-policy knobs are read
// on demand from consult.DefaultLoopPolicy via the consult package.
func consultTargetFor(model consult.RequestedModel) string {
	if t, ok := consultDefaultTargets[model]; ok {
		return t
	}
	return string(model)
}

func runConsult(cmd *cobra.Command, args []string) error {
	if len(args) == 0 && !consultStdin && consultReason == "" {
		return cmd.Help()
	}

	var question string
	if consultStdin {
		if len(args) > 0 || consultReason != "" {
			return fmt.Errorf("cannot combine --stdin with positional question or --reason")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		question = strings.TrimRight(string(data), "\n")
	} else {
		question = strings.TrimSpace(strings.Join(args, " "))
		if question == "" {
			// Fallback to --reason so simple scripts can pass the question
			// in --reason without juggling argv quoting.
			question = strings.TrimSpace(consultReason)
		}
	}

	if question == "" {
		return fmt.Errorf("consult: question is required (pass as argument, --stdin, or --reason)")
	}

	trigger := consult.TriggerClass(strings.ToLower(strings.TrimSpace(consultTrigger)))
	if trigger == "" {
		return fmt.Errorf("consult: --trigger is required (merge_policy | witness_refinery_override | recovery_loop | ambiguous_directive | low_confidence_output)")
	}
	model := consult.RequestedModel(strings.ToLower(strings.TrimSpace(consultModel)))
	if model == "" {
		return fmt.Errorf("consult: --model is required (codex | opus)")
	}

	askedBy := strings.TrimSpace(consultAskedBy)
	if askedBy == "" {
		askedBy = consultActor()
	}

	req := &consult.Request{
		Question:        question,
		TriggerClass:    trigger,
		RequestedModel:  model,
		ContextRefs:     consultContextRefs,
		Options:         consultOptions,
		CurrentDecision: strings.TrimSpace(consultCurrent),
		AskedBy:         askedBy,
		AskedAt:         time.Now().UTC().Format(time.RFC3339),
		RelatedBead:     strings.TrimSpace(consultRelatedBead),
		Fingerprint:     strings.TrimSpace(consultFingerprint),
	}
	if req.Fingerprint == "" {
		req.Fingerprint = consult.Fingerprint(req.Question, req.TriggerClass)
	}
	if err := req.Validate(); err != nil {
		return err
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	if consultDryRun {
		fmt.Printf("Would file consult packet:\n")
		fmt.Printf("  Trigger:        %s\n", req.TriggerClass)
		fmt.Printf("  Model:          %s\n", req.RequestedModel)
		fmt.Printf("  Asked by:       %s\n", req.AskedBy)
		fmt.Printf("  Related bead:   %s\n", emptyDash(req.RelatedBead))
		fmt.Printf("  Fingerprint:    %s\n", req.Fingerprint)
		fmt.Printf("  Question:       %s\n", req.Question)
		if req.CurrentDecision != "" {
			fmt.Printf("  Current:        %s\n", req.CurrentDecision)
		}
		if len(req.Options) > 0 {
			fmt.Printf("  Options:\n")
			for i, opt := range req.Options {
				fmt.Printf("    %d. %s\n", i+1, opt)
			}
		}
		if len(req.ContextRefs) > 0 {
			fmt.Printf("  Context refs:   %s\n", strings.Join(req.ContextRefs, ","))
		}
		fmt.Printf("  Mail target:    %s\n", consultTargetFor(req.RequestedModel))
		return nil
	}

	// Create the consult bead. The title is the question truncated to a
	// bead-friendly length; the full question lives in the description
	// where bd show renders it.
	title := truncateForBeadTitle(req.Question)
	beadsClient := beads.New(beads.ResolveBeadsDir(townRoot))
	fields := &beads.ConsultFields{
		Request: req,
		State:   consult.StateOpen,
	}
	beadID, err := beadsClient.CreateConsultBead(title, fields)
	if err != nil {
		return fmt.Errorf("creating consult bead: %w", err)
	}
	req.BeadID = beadID

	// Best-effort mail to the consulted model's mailbox. We do not block
	// consult creation on this: the bead is durable regardless.
	router := mail.NewRouter(townRoot)
	defer router.WaitPendingNotifications()
	target := consultTargetFor(req.RequestedModel)
	msg := &mail.Message{
		From:    askedBy,
		To:      target,
		Subject: fmt.Sprintf("[consult:%s] %s", req.RequestedModel, req.Question),
		Body:    formatConsultMailBody(req),
		Type:    mail.TypeTask,
		// Treat consult as medium-priority by default — high-priority
		// consults should escalate (gt escalate), not consult.
		Priority: mail.PriorityNormal,
	}
	mailSent := true
	if err := router.Send(msg); err != nil {
		mailSent = false
		style.PrintWarning("consult bead created but mail delivery to %s failed: %v", target, err)
	}

	// Record a loop event so future invocations of the same fingerprint
	// can detect escalation cycles (handles gastown-cet.6.4 acceptance:
	// "repeated same-failure loops trigger escalation").
	detector := consult.NewLoopDetector(townRoot, consult.DefaultLoopPolicy())
	decision, _ := detector.Check(req.Fingerprint, time.Now().UTC())
	// Note: we record-then-check so the new event is included in the count.
	if _, recErr := detector.RecordAndCheck(req.Fingerprint, "consult", beadID, time.Now().UTC()); recErr != nil {
		style.PrintWarning("recording loop event: %v", recErr)
	}

	output := map[string]interface{}{
		"id":          beadID,
		"trigger":     string(req.TriggerClass),
		"model":       string(req.RequestedModel),
		"fingerprint": req.Fingerprint,
		"mail_sent":   mailSent,
		"target":      target,
		"related":     req.RelatedBead,
		"loop": map[string]interface{}{
			"action":    string(decision.Action),
			"count":     decision.Count,
			"threshold": decision.Threshold,
			"window":    decision.Window.String(),
		},
	}
	if consultListJSON {
		out, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("%s Consult packet filed: %s\n", style.Bold.Render("✓"), beadID)
	fmt.Printf("  Trigger:        %s\n", req.TriggerClass)
	fmt.Printf("  Model:          %s\n", req.RequestedModel)
	fmt.Printf("  Mail target:    %s\n", target)
	if mailSent {
		fmt.Printf("  Mail sent:      yes\n")
	} else {
		fmt.Printf("  Mail sent:      no (bead is durable; mail will retry on next consult)\n")
	}
	if req.RelatedBead != "" {
		fmt.Printf("  Related bead:   %s\n", req.RelatedBead)
	}
	fmt.Printf("  Fingerprint:    %s\n", req.Fingerprint)
	if decision.Action == consult.LoopActionConsult || decision.Action == consult.LoopActionEscalate {
		fmt.Printf("  %s Loop detector fired: %d occurrences within %s (action=%s)\n",
			style.Bold.Render("⚠"), decision.Count, decision.Window, decision.Action)
	}
	fmt.Printf("\n  To record a response: gt consult answer %s --decided-by %s --decision ... --rationale ...\n",
		beadID, req.RequestedModel)
	fmt.Printf("  To close and mirror:  gt consult close %s --reason ...\n", beadID)
	return nil
}

func runConsultList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	statusFilter := "open"
	if consultListAll {
		statusFilter = "all"
	}
	out, err := bd.Run("list", "--label=gt:consult", "--status="+statusFilter, "--json")
	if err != nil {
		return fmt.Errorf("listing consult packets: %w", err)
	}
	var issues []*beads.Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return fmt.Errorf("parsing consult packets: %w", err)
	}

	if consultListJSON {
		out, _ := json.MarshalIndent(issues, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	if len(issues) == 0 {
		fmt.Println("No consult packets found")
		return nil
	}
	fmt.Printf("Consult packets (%d):\n\n", len(issues))
	for _, issue := range issues {
		req, _ := beads.ParseConsultFields(issue.Description)
		emoji := "📝"
		switch req.TriggerClass {
		case consult.TriggerMergePolicy:
			emoji = "🔀"
		case consult.TriggerWitnessRefineryOverride:
			emoji = "🛑"
		case consult.TriggerRecoveryLoop:
			emoji = "🔁"
		case consult.TriggerAmbiguousDirective:
			emoji = "❓"
		case consult.TriggerLowConfidenceOutput:
			emoji = "🪫"
		}
		fmt.Printf("  %s %s [%s] %s\n", emoji, issue.ID, issue.Status, issue.Title)
		fmt.Printf("     Trigger: %s | Model: %s | From: %s | %s\n",
			req.TriggerClass, req.RequestedModel, req.AskedBy, formatRelativeTime(issue.CreatedAt))
		fmt.Println()
	}
	return nil
}

func runConsultShow(cmd *cobra.Command, args []string) error {
	consultID := args[0]
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	issue, req, resp, closedBy, closedReason, err := bd.GetConsultBead(consultID)
	if err != nil {
		return fmt.Errorf("getting consult bead: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("consult packet not found: %s", consultID)
	}

	if consultShowJSON {
		data := map[string]interface{}{
			"id":            issue.ID,
			"title":         issue.Title,
			"status":        issue.Status,
			"created_at":    issue.CreatedAt,
			"trigger":       string(req.TriggerClass),
			"model":         string(req.RequestedModel),
			"question":      req.Question,
			"options":       req.Options,
			"current":       req.CurrentDecision,
			"context_refs":  req.ContextRefs,
			"related_bead":  req.RelatedBead,
			"fingerprint":   req.Fingerprint,
			"asked_by":      req.AskedBy,
			"asked_at":      req.AskedAt,
			"response":      resp,
			"closed_by":     closedBy,
			"closed_reason": closedReason,
		}
		out, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("📝 Consult packet: %s\n", issue.ID)
	fmt.Printf("  Status:      %s\n", issue.Status)
	fmt.Printf("  Trigger:     %s\n", req.TriggerClass)
	fmt.Printf("  Model:       %s\n", req.RequestedModel)
	fmt.Printf("  Asked by:    %s at %s\n", req.AskedBy, req.AskedAt)
	if req.RelatedBead != "" {
		fmt.Printf("  Related:     %s\n", req.RelatedBead)
	}
	if req.Fingerprint != "" {
		fmt.Printf("  Fingerprint: %s\n", req.Fingerprint)
	}
	fmt.Println()
	fmt.Printf("  Question:\n    %s\n", req.Question)
	if req.CurrentDecision != "" {
		fmt.Printf("  Mayor's current best guess: %s\n", req.CurrentDecision)
	}
	if len(req.Options) > 0 {
		fmt.Println("  Options:")
		for i, opt := range req.Options {
			fmt.Printf("    %d. %s\n", i+1, opt)
		}
	}
	if len(req.ContextRefs) > 0 {
		fmt.Printf("  Context refs: %s\n", strings.Join(req.ContextRefs, ", "))
	}
	if resp != nil && resp.DecidedBy != "" {
		fmt.Println()
		fmt.Printf("  Response (from %s at %s):\n", resp.DecidedBy, resp.DecidedAt)
		fmt.Printf("    Decision:    %s\n", emptyDash(resp.Decision))
		fmt.Printf("    Rationale:   %s\n", emptyDash(resp.Rationale))
		if resp.Confidence != "" {
			fmt.Printf("    Confidence:  %s\n", resp.Confidence)
		}
	}
	if closedBy != "" {
		fmt.Println()
		fmt.Printf("  Closed by:   %s\n", closedBy)
		if closedReason != "" {
			fmt.Printf("  Reason:      %s\n", closedReason)
		}
	}
	return nil
}

func runConsultAnswer(cmd *cobra.Command, args []string) error {
	consultID := args[0]

	if strings.TrimSpace(consultDecisionBy) == "" {
		return fmt.Errorf("--decided-by is required")
	}
	if strings.TrimSpace(consultDecision) == "" {
		return fmt.Errorf("--decision is required")
	}
	if strings.TrimSpace(consultRationale) == "" {
		return fmt.Errorf("--rationale is required")
	}

	resp := &consult.Response{
		DecidedBy:  strings.TrimSpace(consultDecisionBy),
		DecidedAt:  time.Now().UTC().Format(time.RFC3339),
		Decision:   strings.TrimSpace(consultDecision),
		Rationale:  strings.TrimSpace(consultRationale),
		Confidence: strings.ToLower(strings.TrimSpace(consultConfidence)),
	}
	if err := resp.Validate(); err != nil {
		return err
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	issue, err := bd.GetConsultBeadIssue(consultID)
	if err != nil {
		return fmt.Errorf("getting consult bead: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("consult packet not found: %s", consultID)
	}
	if issue.Status == "closed" {
		return fmt.Errorf("consult packet %s is already closed", consultID)
	}

	if consultDryRun {
		fmt.Printf("Would record answer on %s:\n", consultID)
		fmt.Printf("  Decided by:  %s\n", resp.DecidedBy)
		fmt.Printf("  Decision:    %s\n", resp.Decision)
		fmt.Printf("  Rationale:   %s\n", resp.Rationale)
		if resp.Confidence != "" {
			fmt.Printf("  Confidence:  %s\n", resp.Confidence)
		}
		return nil
	}

	if err := bd.RecordConsultAnswer(consultID, resp); err != nil {
		return fmt.Errorf("recording consult answer: %w", err)
	}

	if consultCloseJSON {
		out, _ := json.MarshalIndent(map[string]interface{}{
			"id":         consultID,
			"decided_by": resp.DecidedBy,
			"decision":   resp.Decision,
			"answered":   true,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	fmt.Printf("%s Consult answer recorded on %s\n", style.Bold.Render("✓"), consultID)
	fmt.Printf("  Decision:  %s\n", resp.Decision)
	fmt.Printf("  Rationale: %s\n", resp.Rationale)
	return nil
}

func runConsultClose(cmd *cobra.Command, args []string) error {
	consultID := args[0]
	if strings.TrimSpace(consultReason) == "" {
		return fmt.Errorf("--reason is required")
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	bd := beads.New(beads.ResolveBeadsDir(townRoot))
	issue, req, resp, _, _, err := bd.GetConsultBead(consultID)
	if err != nil {
		return fmt.Errorf("getting consult bead: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("consult packet not found: %s", consultID)
	}
	if issue.Status == "closed" {
		return fmt.Errorf("consult packet %s is already closed", consultID)
	}

	closer := consultActor()
	if consultDryRun {
		fmt.Printf("Would close %s:\n", consultID)
		fmt.Printf("  Closed by: %s\n", closer)
		fmt.Printf("  Reason:    %s\n", consultReason)
		if req != nil && req.RelatedBead != "" {
			fmt.Printf("  Would mirror onto %s: %s\n", req.RelatedBead, mirrorSummary(resp))
		}
		return nil
	}

	if err := bd.CloseConsultBead(consultID, closer, consultReason, resp); err != nil {
		return fmt.Errorf("closing consult bead: %w", err)
	}

	mirrored := false
	if req != nil && req.RelatedBead != "" {
		if err := bd.MirrorConsultResultOnSource(req.RelatedBead, resp, closer, consultReason, consultID); err != nil {
			style.PrintWarning("consult closed but mirroring onto %s failed: %v", req.RelatedBead, err)
		} else {
			mirrored = true
		}
	}

	if consultCloseJSON {
		out, _ := json.MarshalIndent(map[string]interface{}{
			"id":            consultID,
			"status":        "closed",
			"closed_by":     closer,
			"closed_reason": consultReason,
			"mirrored":      mirrored,
			"related_bead":  req.RelatedBead,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("%s Consult packet closed: %s\n", style.Bold.Render("✓"), consultID)
	fmt.Printf("  Closed by: %s\n", closer)
	fmt.Printf("  Reason:    %s\n", consultReason)
	if req != nil && req.RelatedBead != "" {
		if mirrored {
			fmt.Printf("  Mirrored decision onto %s\n", req.RelatedBead)
		} else {
			fmt.Printf("  Related bead %s not updated (see warning above)\n", req.RelatedBead)
		}
	}
	return nil
}

// formatConsultMailBody renders a consult packet as a mail body for the
// consulted model's mailbox. Kept here (not in the beads package) because
// the body shape is a presentation concern, not a data concern.
func formatConsultMailBody(req *consult.Request) string {
	if req == nil {
		return ""
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Consult packet: %s", emptyDash(req.BeadID)))
	lines = append(lines, fmt.Sprintf("Trigger class:  %s", req.TriggerClass))
	lines = append(lines, fmt.Sprintf("Requested model: %s", req.RequestedModel))
	lines = append(lines, fmt.Sprintf("Asked by:       %s", req.AskedBy))
	lines = append(lines, fmt.Sprintf("Asked at:       %s", req.AskedAt))
	if req.RelatedBead != "" {
		lines = append(lines, fmt.Sprintf("Related bead:   %s", req.RelatedBead))
	}
	lines = append(lines, "")
	lines = append(lines, "Question:")
	lines = append(lines, req.Question)
	if req.CurrentDecision != "" {
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Mayor's current best guess: %s", req.CurrentDecision))
	}
	if len(req.Options) > 0 {
		lines = append(lines, "")
		lines = append(lines, "Options under consideration:")
		for i, opt := range req.Options {
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, opt))
		}
	}
	if len(req.ContextRefs) > 0 {
		lines = append(lines, "")
		lines = append(lines, "Context refs (read in order):")
		for _, ref := range req.ContextRefs {
			lines = append(lines, "  - "+ref)
		}
	}
	lines = append(lines, "")
	lines = append(lines, "---")
	lines = append(lines, "To record a response: gt consult answer <id> --decided-by "+string(req.RequestedModel)+" --decision ... --rationale ...")
	lines = append(lines, "To close:            gt consult close <id> --reason ...")
	return strings.Join(lines, "\n")
}

func truncateForBeadTitle(s string) string {
	const max = 120
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func mirrorSummary(resp *consult.Response) string {
	if resp == nil || resp.DecidedBy == "" {
		return "(no answer recorded)"
	}
	return fmt.Sprintf("model=%s decision=%s rationale=%s",
		resp.DecidedBy, emptyDash(resp.Decision), oneLine(resp.Rationale))
}
