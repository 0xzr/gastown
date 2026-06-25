# Mayor Consult / Escalation Path to Codex and Opus

> Bead: `gastown-cet.6.4`
> Status: implemented in `gt consult` + `internal/consult`
> Owner: Mayor

## Purpose

The Mayor is the Gas Town cross-rig coordinator. Most of the time the Mayor
should stay lightweight — sling work, file escalations, dispatch polecats.
But five classes of decision are too consequential for the Mayor's default
model to guess at:

| Trigger class | Why the Mayor must consult |
|---|---|
| `merge_policy` | A change to town-wide merge / refinery rules affects every rig. The Mayor should not invent a policy under uncertainty. |
| `witness_refinery_override` | Manual Witness or Refinery override (manual bypass, emergency cleanup, hook reset, etc.) bypasses the normal pipeline. A stronger model should weigh in on whether the override is justified. |
| `recovery_loop` | The same failure fingerprint keeps firing — repeated polecat crashes, repeated merge rejections, repeated recovery loops. Blind re-dispatch is the wrong reflex. |
| `ambiguous_directive` | Operator message that the Mayor cannot parse with confidence. The Mayor should not invent an interpretation. |
| `low_confidence_output` | The Mayor itself judges its own model output unreliable (low tool-call success, contradictory evidence, etc.). |

For all five, the Mayor files a durable **consult packet** and routes it
to Codex or Opus. The packet survives polecat restarts and Dolt commits so
the decision, context, and resulting action are all auditable.

## Why a new command, not just `gt escalate`?

`gt escalate` exists for "something is wrong, get a human or another agent
to look at it." It fans out by severity, can route to email/SMS/Slack, and
its primary effect is interruption.

Consult is different:

- **Targeted, not broadcast.** Consult routes to exactly one model mailbox
  (Codex or Opus). It does not page a human, send email, or post to Slack.
- **Durable, not transient.** The packet is a bead, not a transient mail
  subject. The packet is open until the Mayor explicitly closes it.
- **Decision-shaped, not problem-shaped.** Escalations carry a description
  ("disk full"). Consults carry a question, current decision, options, and
  expected answer shape ("Allow / Reject / Allow-with-squash").
- **Mirrored onto the source bead.** When a consult closes, the decision is
  written back onto the source bead's notes so the source bead's audit
  trail carries what was decided and why.

## Workflow

### 1. File a consult

```bash
gt consult "Allow stacked-branch tip-only MRs?" \
  --trigger merge_policy \
  --model opus \
  --related gastown-cet.2.3 \
  --option "Allow" \
  --option "Reject" \
  --option "Allow with squash" \
  --current "Allow with squash (Mayor's best guess)" \
  --context gastown-cet.2 \
  --context hq-try2
```

A bead is created with labels `gt:consult`, `trigger:merge_policy`,
`model:opus`, and the consult-fingerprint. Mail is sent to the configured
target mailbox. The packet is durable regardless of mail delivery.

### 2. Consulted model responds

The operator (or the consulted model's session) runs:

```bash
gt consult answer gastown-cet.6.4-xyz \
  --decided-by opus \
  --decision "Allow with squash" \
  --rationale "Stacked tip-only MRs break publisher state." \
  --confidence high
```

This stamps the bead with the response but leaves it open.

### 3. Mayor closes the packet

```bash
gt consult close gastown-cet.6.4-xyz \
  --reason "acted on Opus recommendation; merged with squash"
```

Closing mirrors the decision onto the related source bead via
`bd update --notes` so the source bead carries:

```
consult_decision: gastown-cet.6.4-xyz consulted_model=opus
decision=Allow with squash confidence=high
rationale=Stacked tip-only MRs break publisher state.
consult_bead=gastown-cet.6.4-xyz closed_by=mayor reason=...
```

If no answer was recorded (e.g., the consulted model never replied), the
close still stamps a `consult_close:` line so the audit trail records that
the packet was filed and closed without a decision.

## Repeated same-failure loop detection

`internal/consult.LoopDetector` watches for repeated identical fingerprints
in `<town-root>/mayor/consult_loops.json`. Default policy:

- 3 occurrences within 30 minutes → recommend `LoopActionConsult`
- 6 occurrences within 30 minutes → recommend `LoopActionEscalate`

Callers (witness, deacon, mayor recovery paths) can plug into the detector
via:

```go
detector := consult.NewLoopDetector(townRoot, consult.DefaultLoopPolicy())
decision, err := detector.RecordAndCheck(fingerprint, "witness", beadID, time.Now())
switch decision.Action {
case consult.LoopActionNone:
    // proceed normally
case consult.LoopActionConsult:
    // file a consult instead of repeating the original action
case consult.LoopActionEscalate:
    // page a human via gt escalate
}
```

The `gt consult` command itself records-and-checks on every invocation, so
operators immediately see whether their consult is part of an escalation
cycle.

## Configuration

Default settings live in code (`consultDefaultTargets`, `consult.DefaultLoopPolicy`).
A future iteration will read overrides from `~/gt/settings/consult.json`:

```json
{
  "type": "consult",
  "version": 1,
  "targets": {
    "codex": "codex",
    "opus": "opus"
  },
  "loop": {
    "threshold": 3,
    "window": "30m",
    "escalate_at": 6
  }
}
```

For now, settings are read on demand from the package defaults so a
town-wide change is a code change.

## Normal dispatch remains lightweight

The five trigger classes are explicit and narrow. Outside of them the
Mayor continues to dispatch normally — file escalations via `gt escalate`,
sling polecats via `gt sling`, etc. Consult is opt-in and the per-call
overhead is one bd create + one mail send.

## Related beads

- `gastown-cet.6.1` — Mayor role routing harness for `umans-glm-5.2`
  (prerequisite; sets up the model registry).
- `gastown-cet.7` / `hq-12zq` — Mayor decision precedence (defer/hold/park/resume).
  Consult does not change decision precedence; it complements it.
- `gastown-wisp-1mr` — sling-context bead for this work.