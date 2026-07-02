package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

func TestProductionChecksRegistered(t *testing.T) {
	checks := ProductionChecks()
	var names []string
	for _, check := range checks {
		names = append(names, check.Name())
	}

	want := []string{
		"prod-dolt-service",
		"prod-dolt-databases",
		"prod-dolt-query-canary",
		"prod-daemon-heartbeat",
		"prod-tmux-ownership",
		"prod-free-space",
		"prod-load-average",
		"prod-reject-ledger",
		"prod-random-dolt-listeners",
		"prod-hardcoded-db-pollution",
		"prod-umans-evidence",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("ProductionChecks names = %#v, want %#v", names, want)
	}
}

func TestProductionDoltDatabasesCheckReportsConfiguredProductionDatabases(t *testing.T) {
	deps := productionDoltDeps{
		configuredDatabases: func(root string) []string {
			if root != "/town" {
				t.Fatalf("configured root = %q, want /town", root)
			}
			return []string{"hq", "gastown", "polybot"}
		},
		listDatabases: func(root string) ([]string, error) {
			if root != "/town" {
				t.Fatalf("list root = %q, want /town", root)
			}
			return []string{"polybot", "gtviz", "hq", "gastown", "bdglobal"}, nil
		},
		protectedDatabases: func() []string {
			return []string{"bdglobal", "beads_global"}
		},
	}

	res := newProductionDoltDatabasesCheck(deps).Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want OK; message=%q details=%v", res.Status, res.Message, res.Details)
	}
	if !strings.Contains(res.Message, "hq,gastown,polybot") {
		t.Fatalf("Message = %q, want configured DB list in production order", res.Message)
	}
	if !detailsContain(res.Details, "Available: bdglobal,gastown,gtviz,hq,polybot") {
		t.Fatalf("Details = %#v, want available DB evidence", res.Details)
	}
}

func TestProductionDoltDatabasesCheckErrorsWhenConfiguredDBMissing(t *testing.T) {
	deps := productionDoltDeps{
		configuredDatabases: func(string) []string {
			return []string{"hq", "gastown", "polybot"}
		},
		listDatabases: func(string) ([]string, error) {
			return []string{"hq", "gastown"}, nil
		},
		protectedDatabases: func() []string { return nil },
	}

	res := newProductionDoltDatabasesCheck(deps).Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error", res.Status)
	}
	if !strings.Contains(res.Message, "polybot") {
		t.Fatalf("Message = %q, want missing DB name", res.Message)
	}
}

func TestProductionLegacyDatabasePollutionCheckRejectsGtMo(t *testing.T) {
	deps := productionDoltDeps{
		configuredDatabases: func(string) []string {
			return []string{"hq", "gastown", "polybot"}
		},
		listDatabases: func(string) ([]string, error) {
			return []string{"hq", "gastown", "polybot", "gt", "mo"}, nil
		},
		protectedDatabases: func() []string { return nil },
	}

	res := newProductionLegacyDatabasePollutionCheck(deps).Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error", res.Status)
	}
	if !strings.Contains(res.Message, "gt,mo") {
		t.Fatalf("Message = %q, want legacy DB names", res.Message)
	}
	if res.FixHint == "" {
		t.Fatal("FixHint should force handoff instead of in-doctor cleanup")
	}
}

func TestProductionLegacyDatabasePollutionCheckAllowsConfiguredProductionDBs(t *testing.T) {
	deps := productionDoltDeps{
		configuredDatabases: func(string) []string {
			return []string{"hq", "gastown", "polybot"}
		},
		listDatabases: func(string) ([]string, error) {
			return []string{"hq", "gastown", "polybot", "gtviz"}, nil
		},
		protectedDatabases: func() []string { return nil },
	}

	res := newProductionLegacyDatabasePollutionCheck(deps).Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want OK; message=%q details=%v", res.Status, res.Message, res.Details)
	}
	if !strings.Contains(res.Message, "no legacy gt/mo") {
		t.Fatalf("Message = %q, want gt/mo clean evidence", res.Message)
	}
}

func TestProductionTmuxOwnershipCheckAcceptsCleanGtOwnedServer(t *testing.T) {
	deps := productionTmuxDeps{
		expectedSocket: func(string) string { return "gt-town-abc123" },
		serverInfo: func(socket string) (*tmux.ServerInfo, error) {
			if socket != "gt-town-abc123" {
				t.Fatalf("socket = %q", socket)
			}
			return &tmux.ServerInfo{
				SocketName:     socket,
				PID:            1234,
				Argv:           "tmux -u -L gt-town-abc123 new-session -d -s gt-tmux-anchor",
				Owner:          "gt",
				TownRoot:       "/town",
				RecordedSocket: socket,
				Origin:         "gt-daemon-bootstrap",
				OriginSession:  "gt-tmux-anchor",
				Sessions:       []string{"hq-mayor", "polybot-refinery"},
			}, nil
		},
		countSockets: func(string) (int, []string, error) {
			return 3, []string{"default", "gt-town-abc123", "personal"}, nil
		},
		socketDir: func() string { return "/tmp/tmux-test" },
	}

	res := newProductionTmuxOwnershipCheck(deps).Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want OK; message=%q details=%v", res.Status, res.Message, res.Details)
	}
	if !strings.Contains(res.Message, "gt-owned") {
		t.Fatalf("Message = %q, want ownership evidence", res.Message)
	}
}

func TestProductionTmuxOwnershipCheckRejectsTestOrigin(t *testing.T) {
	deps := productionTmuxDeps{
		expectedSocket: func(string) string { return "gt-town-abc123" },
		serverInfo: func(socket string) (*tmux.ServerInfo, error) {
			return &tmux.ServerInfo{
				SocketName:     socket,
				PID:            1234,
				Argv:           "tmux -u new-session -d -s gt-test-modeA-2 -c /tmp",
				Owner:          "gt",
				TownRoot:       "/town",
				RecordedSocket: socket,
				Origin:         "gt-daemon-adopt",
				OriginPID:      "1234",
				OriginArgv:     "tmux -u new-session -d -s gt-test-modeA-2 -c /tmp",
			}, nil
		},
		countSockets: func(string) (int, []string, error) { return 2, nil, nil },
		socketDir:    func() string { return "/tmp/tmux-test" },
	}

	res := newProductionTmuxOwnershipCheck(deps).Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error", res.Status)
	}
	if !detailsContain(res.Details, "test/refinery transient") {
		t.Fatalf("Details = %#v, want bad-origin evidence", res.Details)
	}
}

func TestProductionTmuxOwnershipCheckRejectsSocketPileup(t *testing.T) {
	deps := productionTmuxDeps{
		expectedSocket: func(string) string { return "gt-town-abc123" },
		serverInfo: func(socket string) (*tmux.ServerInfo, error) {
			return &tmux.ServerInfo{
				SocketName:     socket,
				PID:            1234,
				Owner:          "gt",
				TownRoot:       "/town",
				RecordedSocket: socket,
				Origin:         "gt-daemon-bootstrap",
			}, nil
		},
		countSockets: func(string) (int, []string, error) {
			return productionTmuxSocketFail, []string{"gt-town-abc123", "gt-test-a"}, nil
		},
		socketDir: func() string { return "/tmp/tmux-test" },
	}

	res := newProductionTmuxOwnershipCheck(deps).Run(&CheckContext{TownRoot: "/town"})
	if res.Status != StatusError {
		t.Fatalf("Status = %v, want Error", res.Status)
	}
	if !strings.Contains(res.Message, "not cleanly gt-owned") {
		t.Fatalf("Message = %q, want dirty tmux state", res.Message)
	}
}

func TestProductionUMANSEvidenceCheckReadsOnlyServiceAndLogs(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	logDir := filepath.Join(home, "umans-dash", ".logs")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeExecutable(t, filepath.Join(binDir, "umans-proxy-canary.sh"), "#!/usr/bin/env bash\nexit 99\n")
	writeExecutable(t, filepath.Join(binDir, "umans-token-lint.sh"), "#!/usr/bin/env bash\nexit 99\n")
	writeLog(t, filepath.Join(logDir, "umans-canary.log"), "2026-07-02T02:40:02Z OK\n")
	writeLog(t, filepath.Join(logDir, "umans-token-lint.log"), "2026-07-02T02:26:13Z OK healthz api_validate settings_glm52 settings_kimi27 raw_limit=4 headroom=1 external=0 limit=3\n")

	res := newProductionUMANSEvidenceCheck(productionUMANSDeps{
		homeDir: func() (string, error) { return home, nil },
		now:     func() time.Time { return time.Date(2026, 7, 2, 2, 45, 0, 0, time.UTC) },
		serviceStatus: func(ctx context.Context, service string) (map[string]string, error) {
			if service != "umans-proxy.service" {
				t.Fatalf("service = %q, want umans-proxy.service", service)
			}
			return map[string]string{
				"LoadState":   "loaded",
				"ActiveState": "active",
				"SubState":    "running",
				"Result":      "success",
				"ExecMainPID": "1234",
			}, nil
		},
		readLastLine: readLastLogLine,
		statFile:     os.Stat,
	}).Run(&CheckContext{TownRoot: "/town"})

	if res.Status != StatusOK {
		t.Fatalf("Status = %v, want OK; message=%q details=%v", res.Status, res.Message, res.Details)
	}
	if !detailsContain(res.Details, "Service: load=loaded active=active sub=running") {
		t.Fatalf("Details = %#v, want service evidence", res.Details)
	}
	if !detailsContain(res.Details, "canary log: 2026-07-02T02:40:02Z OK") {
		t.Fatalf("Details = %#v, want canary log evidence", res.Details)
	}
	if !detailsContain(res.Details, "token-lint log: 2026-07-02T02:26:13Z OK") {
		t.Fatalf("Details = %#v, want token-lint log evidence", res.Details)
	}
}

func TestProductionUMANSEvidenceCheckWarnsWhenEvidenceUnavailable(t *testing.T) {
	res := newProductionUMANSEvidenceCheck(productionUMANSDeps{
		homeDir: func() (string, error) { return "/missing-home", nil },
		now:     time.Now,
		serviceStatus: func(context.Context, string) (map[string]string, error) {
			return nil, errors.New("systemctl unavailable")
		},
		readLastLine: readLastLogLine,
		statFile:     os.Stat,
	}).Run(&CheckContext{TownRoot: "/town"})

	if res.Status != StatusWarning {
		t.Fatalf("Status = %v, want Warning; message=%q details=%v", res.Status, res.Message, res.Details)
	}
	if !detailsContain(res.Details, "systemctl unavailable") {
		t.Fatalf("Details = %#v, want service warning", res.Details)
	}
}

func detailsContain(details []string, needle string) bool {
	for _, detail := range details {
		if strings.Contains(detail, needle) {
			return true
		}
	}
	return false
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeLog(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
