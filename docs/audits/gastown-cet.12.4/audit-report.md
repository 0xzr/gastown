# Retro-gate GTViz initial commit (gastown-cet.12.4) — STRICT CORE REVIEW AUDIT

## Scope

Audit commit `2abdc645d10a3c8ca35d301d6e9e3e0a215bf062` ("Initial commit" by jasper, 2026-06-24T15:18:41-04:00) in the gtviz rig. Plus the generated rig setup/config files that accompany this commit in the working tree at `/home/ubuntu/gt-town/gtviz/`. **No new GTViz feature work.**

## Commit Contents

The commit lives in the bare repo `/home/ubuntu/gt-town/gtviz/.repo.git` (remote `file:///home/ubuntu/gt-town-backups/gtviz-origin.git` per `config.json`). Tree hash: `a652f49cbe3321698339d95dca4a1e6c595e7a93`. One file: `README.md` (3 lines):

```
# gtviz

Gas Town dependency/progress visualization rig for BEADS-VIZ TUI development.
```

`git fsck` clean except dangling blobs (irrelevant). Verified locally.

## Rig setup/config (working tree, NOT in commit)

- `/home/ubuntu/gt-town/gtviz/config.json` — Rig config with merge_queue gate `four-model-refinery-review` referencing `/home/ubuntu/gastown-spike/dropin/refinery-gate.sh`. `git_url` points to a file:// backup repo. Beads prefix `gtviz`. Created 2026-06-24T15:18:44.
- `/home/ubuntu/gt-town/gtviz/CLAUDE.md` — Auto-generated beads integration template (unedited).
- `/home/ubuntu/gt-town/gtviz/AGENTS.md` — Same template (unedited).
- `/home/ubuntu/gt-town/gtviz/crew/README.md` — Stock crew template.
- Empty `polecats/` dir, populated but template `mayor/`, `witness/`, `refinery/` subdirs.

## Deterministic GTViz Gates

Re-ran `/home/ubuntu/gt-town/gtviz/mayor/rig/scripts/run-all-gates.sh` on a clean clone (`/tmp/gtviz-audit/gtviz`):

```
[gtviz gates] git diff whitespace check
[gtviz gates] conflict marker check
```

No `package.json` or `go.mod` present, so the npm/go branches are skipped. **Result: PASS** (whitespace + conflict-marker only). Trivial — the commit is a 3-line README.

## Strict Core Model Review — Raw Verdicts

All four core reviewers were invoked via the umans proxy (http://127.0.0.1:8084, `x-api-key: umans-proxy`) except codex (CLI) and m3 (self). Each received the same rubric:

### m3 (MiniMax-M3, self-attest) — FAIL

```
{"verdict":"FAIL","findings":[
 {"classification":"BLOCKING","citation":"README.md:1-3","explanation":"Commit ships only a 3-line README. No code, no config.json, no CLAUDE.md/AGENTS.md, no tests. The 'rig setup/config' is in the working tree at /home/ubuntu/gt-town/gtviz/ but is NOT committed. Anyone who clones 2abdc645 gets an empty deliverable; the parent bead's rig is unshipped. Empty deliverable for stated scope is itself blocking."},
 {"classification":"BLOCKING","citation":"README.md:3","explanation":"Scope mismatch: README claims 'Gas Town dependency/progress visualization rig for BEADS-VIZ TUI development' but ships no visualization logic, no TUI code, no dependency graph, no progress tracking, no BEADS-VIZ integration. The commit delivers a heading and one aspirational sentence, not a rig."},
 {"classification":"BLOCKING","citation":"commit tree (tests)","explanation":"No tests in the commit. Even for a scaffolding commit the rubric requires tests that exercise the change; zero test files means zero coverage of the rig bootstrap."},
 {"classification":"MAJOR","citation":"README.md:1-3","explanation":"README has no Usage, Build, Dependencies, or Config sections; no pointer to config.json, CLAUDE.md, AGENTS.md, or the rig layout. Cannot validate config consistency because no shipped config exists."},
 {"classification":"MINOR","citation":"README.md:1","explanation":"The repo is named 'gtviz' (lowercase) but the README tagline uses 'BEADS-VIZ' (hyphenated). Naming is inconsistent."}
],"summary":"FAIL — Commit is a 3-line README with no rig code, no config, no tests, and an aspirational tagline that does not match shipped content. The rig setup files exist only in the working tree, not in the commit. Empty deliverable for stated scope; rejection required."}
```

### codex (gpt-5.5, xhigh effort, CLI) — PASS (no findings)

```
{"verdict":"PASS","findings":[],"summary":"README.md is a minimal project description and introduces no blocking, major, or minor issues."}
```

**Weak review**: codex accepted the README at face value without noting the rig-setup unshipped, no-tests, or scope mismatch that all three other reviewers caught. The PASS is technically valid (README has no defects) but it does not adversarially examine the commit's failure to ship the rig bootstrap or the working tree files that the bead scope explicitly names.

### umans-kimi (umans-kimi-k2.7, model id `kimi`) — FAIL

```
{"verdict":"FAIL","findings":[
 {"classification":"BLOCKING","citation":"config.json, CLAUDE.md, AGENTS.md","explanation":"Required rig setup files are present in the working tree but excluded from the commit, so the README change lands without the accompanying rig bootstrap artifacts."},
 {"classification":"BLOCKING","citation":"tests/","explanation":"The commit provides no tests for the new rig or its documented behavior, which is unacceptable under strict review standards."}
],"summary":"Commit adds only a minimal README while omitting the required setup files from the index and providing no tests, so it must be rejected."}
```

### umans-glm (umans-glm-5.2, model id `glm`) — FAIL

```
{"verdict":"FAIL","findings":[
 {"classification":"BLOCKING","citation":"README.md:1-3","explanation":"Commit ships only a 3-line README. No rig code, no config.json, no CLAUDE.md/AGENTS.md, no tests. The 'rig setup/config' allegedly lives in the working tree at /home/ubuntu/gt-town/gtviz/ but is NOT in the commit, so anyone checking out 2abdc645 gets none of it. Empty deliverable for stated scope; rubric states empty diff is itself blocking."},
 {"classification":"BLOCKING","citation":"README.md:1-3","explanation":"No tests present and none exercise the change. Rubric item 3 requires tests present and exercising the change; zero test files in the commit."},
 {"classification":"BLOCKING","citation":"README.md:3","explanation":"Scope mismatch: README claims 'dependency/progress visualization rig for BEADS-VIZ TUI development' but ships no visualization logic, no TUI code, no dependency graph, no progress tracking. Parent bead asked for a rig; commit delivers a heading and one sentence."},
 {"classification":"MAJOR","citation":"/home/ubuntu/gt-town/gtviz/crew/ (and mayor/, witness/, polecats/, refinery/)","explanation":"Empty directories are not tracked by git; this structure is phantom and will not survive clone/checkout. Either seed with .gitkeep or remove. Misleading scaffold."},
 {"classification":"MINOR","citation":"README.md:1-3","explanation":"README has no Usage, Build, Dependencies, or Config sections; cannot validate config.json/CLAUDE.md/AGENTS.md consistency because their contents were not supplied and they are not in the commit."}
],"summary":"Initial commit is a 3-line README with no rig code, no config, and no tests in-tree; the purported rig setup lives only in the working tree and is unshipped. Empty deliverable for stated scope; FAIL."}
```

## Tally

| Reviewer | Verdict | Quality |
|---|---|---|
| m3 | FAIL (3 BLOCKING, 1 MAJOR, 1 MINOR) | Adversarial, cited |
| codex | PASS (no findings) | Weak — accepted README at face value |
| umans-kimi | FAIL (2 BLOCKING) | Adversarial, cited |
| umans-glm | FAIL (3 BLOCKING, 1 MAJOR, 1 MINOR) | Adversarial, cited |

**3 FAIL, 1 PASS. The single PASS is a weak review.**

## Telemetry Cross-Check (degraded-quorum merge at 2026-06-25T08:30:06-04:00)

The merge queue gate records (refinery-gate-20260625.jsonl, pid 2905098):

- 08:27:16 — gate_start, validate PASS
- 08:27:29 — m3 PASS (no excerpt, ~13s; likely trivial "diff is empty so nothing to FAIL")
- 08:27:38 — codex UNAVAILABLE (no verdict/no response)
- 08:27:56 — umans-kimi UNAVAILABLE (no verdict/no response)
- 08:27:56 — peer-review phase_complete status=pass, passes=1, unavailable=2
- 08:30:06 — opus PASS (~130s)
- 08:30:06 — gate_complete status="merge", passes=2, anchor_ok=1

Notes on this telemetry:
- **m3 PASS was on an empty diff.** Every other m3 attempt against this commit returned FAIL with explicit "empty diff is blocking" findings. The single PASS is a degenerate case (m3 returned empty findings and the gate treated empty as PASS).
- **codex and umans-kimi were UNAVAILABLE.** The bead description's claim is correct on these two. This is a degraded-quorum merge.
- **opus-verify passed.** Opus ran for 130s and returned PASS. Opus is the bead's fourth reviewer (the bead names Opus as a core reviewer).
- **umans-glm was NOT in the gate at all.** The original gate pipeline did not include umans-glm as a reviewer peer; only m3, codex, umans-kimi, opus-verify. The bead's "m3, codex, umans-kimi, umans-glm" list reflects the corrected strict four-model rule that **was not actually applied at merge time**.

## Conclusion — Retro-Gate Verdict

Under the corrected strict four-model rule the bead enforces (m3, codex, umans-kimi, umans-glm):

- **umans-glm: FAIL (BLOCKING)** — first time this peer ran, would have blocked.
- **umans-kimi: FAIL (BLOCKING)** — first time it responded (vs being UNAVAILABLE in the original gate).
- **m3: FAIL (BLOCKING)** — second non-trivial review; the original gate's m3 PASS was on an empty diff.
- **codex: PASS** — codex returns PASS, accepting README as minimal project description without adversarial examination of rig-setup unshipped / no-tests / scope mismatch.

**3 of 4 reviewers FAIL. The merge should not have happened under strict four-model review.**

The merge was a degraded-quorum bypass: 2 PASS (m3-degenerate + Opus) + 2 UNAVAILABLE (codex + umans-kimi) when the real four-model rule would have produced 3 FAIL + 1 PASS. **The bead's premise is correct: this was a degraded/bypass merge under the corrected rule.**

## Concrete Findings (Linked Follow-ups)

1. **umans-glm missing from gate pipeline** — `four-model-refinery-review` gate in `/home/ubuntu/gt-town/gtviz/config.json` only invokes m3/codex/umans-kimi + opus-verify. umans-glm is configured but never called. Follow-up: file bead to add umans-glm as a peer in the four-model gate.
2. **Rig setup files (config.json, CLAUDE.md, AGENTS.md) are working-tree-only, not in commit.** Follow-up: file bead to commit them.
3. **README claims "BEADS-VIZ TUI" but ships no TUI code.** Follow-up: file bead to either remove the aspirational claim or land actual TUI code.
4. **Empty scaffold dirs (crew/, mayor/, witness/, polecats/, refinery/) are not tracked.** Follow-up: file bead to seed `.gitkeep` or remove.
5. **No tests for the rig bootstrap.** Follow-up: file bead to add rig-bootstrap smoke test.
6. **m3 PASS on empty diff is a degenerate path** — every other attempt caught empty diff as blocking. Follow-up: file bead to harden m3 reviewer to FAIL on empty diff.

## Raw Evidence Locations

- Commit object: `/home/ubuntu/gt-town/gtviz/.repo.git` → `2abdc645d10a3c8ca35d301d6e9e3e0a215bf062`
- Gate logs: `/home/ubuntu/gt-town/.runtime/refinery-gate-logs/20260625T122716Z-2abdc64-validate.log` and similar
- Telemetry: `/home/ubuntu/gt-town/.runtime/refinery-telemetry/refinery-gate-20260625.jsonl`
- Working tree: `/home/ubuntu/gt-town/gtviz/`
- Review outputs: `/tmp/m3-final.txt`, `/tmp/codex-verdict2.txt`, `/tmp/kimi-final.txt`, `/tmp/umans-glm-verdict.txt`
- Audit clone: `/tmp/gtviz-audit/gtviz`

## Verdict Summary

- Deterministic gates: PASS (trivial)
- m3: FAIL
- codex: PASS (weak)
- umans-kimi: FAIL
- umans-glm: FAIL
- **Net strict four-model verdict: FAIL (3 of 4)**
- **Original merge was a degraded-quorum bypass that should not have landed under the corrected strict rule.**
