package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// This file is the dry-run/test harness for gastown-cet.6.1: "Mayor role
// routing harness for umans-glm-5.2".
//
// Goal: prove the Mayor role can be configured for a umans-glm-5.2 agent and
// rolled back to codex WITHOUT breaking gt prime, hooks, mail, town-level bead
// writes, or rig routing. No live Mayor role switch occurs — every assertion
// runs against an isolated t.TempDir() town and a test-only registered agent.
//
// umans-glm-5.2 follows the AgentGroqCompound precedent: it is the Claude CLI
// acting as an SDK proxy, with the backend redirected via ANTHROPIC_* env
// overrides. Because the transport is the Claude binary, Gas Town hooks,
// session tracking, tmux readiness, and Claude-SDK lifecycle events all work
// identically to the standard claude preset — which is exactly what the
// harness asserts.

// glmAgentName is the custom agent name used to route the Mayor to umans-glm-5.2.
const glmAgentName = "umans-glm-5.2"

// glmModelID is the model selector passed to the Claude CLI.
const glmModelID = "umans-glm-5.2"

// registerGLMAgentForTesting registers a Claude-CLI-routed umans-glm-5.2 agent
// preset and returns a teardown function. The preset mirrors AgentGroqCompound:
// the Claude binary stays the transport, so ProcessNames remain ["node","claude"]
// and witness liveness detection is unaffected.
func registerGLMAgentForTesting(t *testing.T) func() {
	t.Helper()
	RegisterAgentForTesting(glmAgentName, AgentPresetInfo{
		Name:         AgentPreset(glmAgentName),
		Command:      "claude",
		Args:         []string{"--dangerously-skip-permissions"},
		ProcessNames: []string{"node", "claude"},
		SessionIDEnv: "CLAUDE_SESSION_ID",
		ResumeFlag:   "--resume",
		ResumeStyle:  "flag",
		// Route the Claude SDK proxy to the umans GLM endpoint. The API key is
		// resolved from the shell env at spawn time, never stored in config.
		Env: map[string]string{
			"ANTHROPIC_BASE_URL": "https://api.umans.ai/v1",
			"ANTHROPIC_MODEL":    glmModelID,
			"ANTHROPIC_API_KEY":  "$UMANS_API_KEY",
		},
		PromptMode:        "arg",
		ConfigDirEnv:      "CLAUDE_CONFIG_DIR",
		ConfigDir:         ".claude",
		HooksProvider:     "claude",
		HooksDir:          ".claude",
		HooksSettingsFile: "settings.json",
		ReadyPromptPrefix: "❯ ",
		ReadyDelayMs:      10000,
		InstructionsFile:  "CLAUDE.md",
	})
	return func() { ResetRegistryForTesting() }
}

// isolatedMayorTown builds an isolated town + rig layout for Mayor routing
// tests. The Mayor is town-scoped, so rigPath is empty in the resolution path;
// a rig dir is still created so rig routing can be exercised in rollback checks.
func isolatedMayorTown(t *testing.T) (townRoot, rigPath string) {
	t.Helper()
	townRoot = t.TempDir()
	rigPath = filepath.Join(townRoot, "gastown")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	// Stub the binaries that resolution validates against.
	binDir := t.TempDir()
	for _, name := range []string{"claude", "codex", "gemini"} {
		writeAgentStub(t, binDir, name)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return townRoot, rigPath
}

// TestMayorRoleRouting_GLMResolvesAndPreservesClaudeTransport proves that
// wiring RoleAgents["mayor"] = "umans-glm-5.2" resolves to the Claude CLI with
// the GLM backend env overrides, and that the Claude transport (hooks, session,
// process names) is preserved — so gt prime, hooks, and liveness still work.
func TestMayorRoleRouting_GLMResolvesAndPreservesClaudeTransport(t *testing.T) {
	defer registerGLMAgentForTesting(t)()
	townRoot, rigPath := isolatedMayorTown(t)

	// Configure town settings: mayor -> umans-glm-5.2, default codex.
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "codex"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor: glmAgentName,
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Mayor is town-scoped: BuildStartupCommand with GT_ROLE=mayor and no
	// rigPath resolves via the town RoleAgents map (mirrors how the daemon
	// launches the mayor session). GT_ROOT must be set to the harness town —
	// this is exactly what AgentEnv does for the real mayor at spawn time.
	mayorEnv := map[string]string{
		"GT_ROLE": constants.RoleMayor,
		"GT_ROOT": townRoot,
	}
	cmd := BuildStartupCommand(mayorEnv, "", "")

	// 1. Resolved agent is the GLM agent.
	rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, "")
	if rc == nil {
		t.Fatal("ResolveRoleAgentConfig returned nil for mayor")
	}
	if rc.ResolvedAgent != glmAgentName {
		t.Errorf("ResolvedAgent = %q, want %q", rc.ResolvedAgent, glmAgentName)
	}
	// 2. Transport is the Claude binary (hooks/session/liveness ride on this).
	if rc.Command != "claude" {
		t.Errorf("Command = %q, want %q (Claude transport must be preserved)", rc.Command, "claude")
	}
	// 3. GLM backend env overrides are present in the resolved config.
	if got := rc.Env["ANTHROPIC_MODEL"]; got != glmModelID {
		t.Errorf("ANTHROPIC_MODEL = %q, want %q", got, glmModelID)
	}
	if got := rc.Env["ANTHROPIC_BASE_URL"]; !strings.Contains(got, "umans.ai") {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want umans.ai endpoint", got)
	}

	// 4. The startup command carries the GLM model selection and the Claude
	//    binary (so hooks + prime identity path are intact).
	if !strings.Contains(cmd, "claude") {
		t.Errorf("startup command missing claude transport: %q", cmd)
	}
	if !strings.Contains(cmd, glmModelID) {
		t.Errorf("startup command missing GLM model %q: %q", glmModelID, cmd)
	}
	// 5. GT_AGENT is exported so witness liveness (IsAgentAlive) detects the
	//    custom agent name, and GT_PROCESS_NAMES stays ["node","claude"] so
	//    non-Claude-process auto-nuke does not fire.
	if !strings.Contains(cmd, "GT_AGENT="+glmAgentName) {
		t.Errorf("startup command missing GT_AGENT=%s: %q", glmAgentName, cmd)
	}
	if !strings.Contains(cmd, "GT_PROCESS_NAMES=node,claude") {
		t.Errorf("startup command missing GT_PROCESS_NAMES=node,claude: %q", cmd)
	}
}

// TestMayorRoleRouting_RollbackToCodex proves that removing the mayor override
// (or pointing it back at codex) restores the codex command and clears the GLM
// env overrides — without leaving any GLM state behind.
func TestMayorRoleRouting_RollbackToCodex(t *testing.T) {
	defer registerGLMAgentForTesting(t)()
	townRoot, rigPath := isolatedMayorTown(t)

	// Start with mayor routed to GLM.
	glmAware := NewTownSettings()
	glmAware.DefaultAgent = "codex"
	glmAware.RoleAgents = map[string]string{
		constants.RoleMayor: glmAgentName,
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), glmAware); err != nil {
		t.Fatalf("SaveTownSettings (glm): %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Sanity: GLM is active before rollback.
	pre := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, "")
	if pre.ResolvedAgent != glmAgentName {
		t.Fatalf("pre-rollback ResolvedAgent = %q, want %q", pre.ResolvedAgent, glmAgentName)
	}

	// Rollback: drop the mayor override entirely so it falls back to default
	// (codex). This is the documented rollback path — delete the override,
	// do not switch live without operator approval.
	rolled := NewTownSettings()
	rolled.DefaultAgent = "codex"
	if err := SaveTownSettings(TownSettingsPath(townRoot), rolled); err != nil {
		t.Fatalf("SaveTownSettings (rolled): %v", err)
	}

	post := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, "")
	if post.ResolvedAgent != "codex" {
		t.Errorf("post-rollback ResolvedAgent = %q, want codex", post.ResolvedAgent)
	}
	if post.Command != "codex" {
		t.Errorf("post-rollback Command = %q, want codex", post.Command)
	}
	// No GLM env leakage after rollback.
	if v, ok := post.Env["ANTHROPIC_MODEL"]; ok && strings.Contains(v, "glm") {
		t.Errorf("post-rollback env leaked GLM model %q", v)
	}

	cmd := BuildStartupCommand(map[string]string{
		"GT_ROLE": constants.RoleMayor,
		"GT_ROOT": townRoot,
	}, "", "")
	if !strings.Contains(cmd, "codex") {
		t.Errorf("post-rollback startup missing codex: %q", cmd)
	}
	if strings.Contains(cmd, glmModelID) {
		t.Errorf("post-rollback startup leaked GLM model %q: %q", glmModelID, cmd)
	}
	if strings.Contains(cmd, "umans.ai") {
		t.Errorf("post-rollback startup leaked umans base URL: %q", cmd)
	}
}

// TestMayorRoleRouting_NoLiveMayorSwitch verifies the harness never touches the
// real town root or live settings: every town it builds is a unique temp dir,
// and the live GT_ROOT (if any) is not consulted by resolution.
func TestMayorRoleRouting_NoLiveMayorSwitch(t *testing.T) {
	defer registerGLMAgentForTesting(t)()
	townRoot, _ := isolatedMayorTown(t)

	// Point a (fake) live root at a sentinel the GLM config does not contain.
	t.Setenv("GT_ROOT", "/nonexistent/live-mayor-root")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "codex"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor: glmAgentName,
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Resolution must use the harness town, NOT the sentinel GT_ROOT.
	rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, "")
	if rc.ResolvedAgent != glmAgentName {
		t.Errorf("ResolvedAgent = %q, want %q (harness town must win over live GT_ROOT)",
			rc.ResolvedAgent, glmAgentName)
	}

	// BuildStartupCommand honors the explicit GT_ROOT in the env map over the
	// ambient sentinel. This mirrors AgentEnv: the daemon sets GT_ROOT to the
	// launch town, so a stray ambient GT_ROOT cannot redirect the mayor.
	cmd := BuildStartupCommand(map[string]string{
		"GT_ROLE": constants.RoleMayor,
		"GT_ROOT": townRoot,
	}, "", "")
	if !strings.Contains(cmd, "GT_AGENT="+glmAgentName) {
		t.Errorf("ambient GT_ROOT leaked: expected GT_AGENT=%s in %q", glmAgentName, cmd)
	}
	if strings.Contains(cmd, "/nonexistent/live-mayor-root") {
		t.Errorf("ambient sentinel GT_ROOT leaked into command: %q", cmd)
	}
}

// TestMayorRoleRouting_TownBeadWritesUnaffected proves that bead/rig routing is
// not disturbed by the mayor agent override: the role is still "mayor", the
// scope is still "town", and the town settings persist/round-trip the override
// faithfully (so town-level bead writes and routing see a consistent role).
func TestMayorRoleRouting_TownBeadWritesUnaffected(t *testing.T) {
	defer registerGLMAgentForTesting(t)()
	townRoot, _ := isolatedMayorTown(t)

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "codex"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor: glmAgentName,
	}
	path := TownSettingsPath(townRoot)
	if err := SaveTownSettings(path, townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Round-trip: the override persists on disk (survives the prime/daemon reload).
	loaded, err := LoadOrCreateTownSettings(path)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}
	if got := loaded.RoleAgents[constants.RoleMayor]; got != glmAgentName {
		t.Errorf("round-tripped RoleAgents[mayor] = %q, want %q", got, glmAgentName)
	}
	if loaded.DefaultAgent != "codex" {
		t.Errorf("DefaultAgent = %q, want codex", loaded.DefaultAgent)
	}

	// Role definition is independent of the agent override: the mayor role
	// still resolves to scope=town from embedded defaults, so town-level bead
	// writes and routing are structurally unchanged.
	def, err := LoadRoleDefinition(townRoot, "", constants.RoleMayor)
	if err != nil {
		t.Fatalf("LoadRoleDefinition mayor: %v", err)
	}
	if def.Role != constants.RoleMayor {
		t.Errorf("role = %q, want mayor", def.Role)
	}
	if def.Scope != "town" {
		t.Errorf("scope = %q, want town (bead write path depends on this)", def.Scope)
	}
}
