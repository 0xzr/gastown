package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/formula"
)

func TestDoctorDogInterval(t *testing.T) {
	// Default interval
	if got := doctorDogInterval(nil); got != defaultDoctorDogInterval {
		t.Errorf("expected default interval %v, got %v", defaultDoctorDogInterval, got)
	}

	// Custom interval
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoctorDog: &DoctorDogConfig{
				Enabled:     true,
				IntervalStr: "10m",
			},
		},
	}
	if got := doctorDogInterval(config); got != 10*time.Minute {
		t.Errorf("expected 10m interval, got %v", got)
	}

	// Invalid interval falls back to default
	config.Patrols.DoctorDog.IntervalStr = "invalid"
	if got := doctorDogInterval(config); got != defaultDoctorDogInterval {
		t.Errorf("expected default interval for invalid config, got %v", got)
	}
}

func TestDoctorDogDatabases(t *testing.T) {
	// Default databases come from town metadata/config, not a stale hardcoded
	// hq/gt/mo list.
	dbs := doctorDogDatabases(nil, t.TempDir())
	if len(dbs) != 1 || dbs[0] != "hq" {
		t.Errorf("expected default town database [hq], got %#v", dbs)
	}

	// Custom databases
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoctorDog: &DoctorDogConfig{
				Enabled:   true,
				Databases: []string{"hq", "beads"},
			},
		},
	}
	dbs = doctorDogDatabases(config, t.TempDir())
	if len(dbs) != 2 {
		t.Errorf("expected 2 custom databases, got %d", len(dbs))
	}
}

func TestDoctorDogDatabasesUsesRegisteredRigMetadata(t *testing.T) {
	townRoot := t.TempDir()
	writeDoctorDogMetadata(t, filepath.Join(townRoot, ".beads"), "hq")
	writeDoctorDogMetadata(t, filepath.Join(townRoot, "gastown", ".beads"), "gastown")
	writeDoctorDogMetadata(t, filepath.Join(townRoot, "polybot", ".beads"), "polybot")
	writeDoctorDogMetadata(t, filepath.Join(townRoot, "gtviz", ".beads"), "gtviz")
	writeDoctorDogRigs(t, townRoot, map[string]string{
		"gastown": "gt-",
		"polybot": "polybot",
	})

	got := doctorDogDatabases(nil, townRoot)
	want := []string{"hq", "gastown", "polybot"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("doctorDogDatabases() = %#v, want %#v", got, want)
	}
}

func TestDoctorDogDatabasesFallsBackToRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeDoctorDogMetadata(t, filepath.Join(townRoot, ".beads"), "hq")
	writeDoctorDogRigs(t, townRoot, map[string]string{
		"laneassist": "la-",
	})

	got := doctorDogDatabases(nil, townRoot)
	want := []string{"hq", "la"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("doctorDogDatabases() = %#v, want %#v", got, want)
	}
}

func TestDoctorDogFormulaConsumesDatabasesVariable(t *testing.T) {
	t.Parallel()

	content, err := formula.GetEmbeddedFormulaContent("mol-dog-doctor")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(mol-dog-doctor): %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"[vars.databases]",
		"{{databases}}",
		"Do not probe\nlegacy database names such as `gt` or `mo`",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor formula missing %q", want)
		}
	}
}

func TestIsPatrolEnabled_DoctorDog(t *testing.T) {
	// Nil config: disabled (opt-in patrol)
	if IsPatrolEnabled(nil, "doctor_dog") {
		t.Error("expected doctor_dog to be disabled with nil config")
	}

	// Empty patrols: disabled
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{},
	}
	if IsPatrolEnabled(config, "doctor_dog") {
		t.Error("expected doctor_dog to be disabled by default")
	}

	// Explicitly enabled
	config.Patrols.DoctorDog = &DoctorDogConfig{Enabled: true}
	if !IsPatrolEnabled(config, "doctor_dog") {
		t.Error("expected doctor_dog to be enabled when configured")
	}

	// Explicitly disabled
	config.Patrols.DoctorDog = &DoctorDogConfig{Enabled: false}
	if IsPatrolEnabled(config, "doctor_dog") {
		t.Error("expected doctor_dog to be disabled when explicitly disabled")
	}
}

func TestDoctorDogDefaultThresholds(t *testing.T) {
	// Verify default thresholds are sane
	if defaultDoctorDogLatencyAlertMs <= 0 {
		t.Error("latency alert threshold must be positive")
	}
	if defaultDoctorDogOrphanAlertCount <= 0 {
		t.Error("orphan alert count must be positive")
	}
	if defaultDoctorDogBackupStaleSeconds <= 0 {
		t.Error("backup stale threshold must be positive")
	}

	// Verify defaults match spec: latency > 5s, orphans > 20, backup > 1hr
	if defaultDoctorDogLatencyAlertMs != 5000.0 {
		t.Errorf("expected latency alert at 5000ms, got %.0f", defaultDoctorDogLatencyAlertMs)
	}
	if defaultDoctorDogOrphanAlertCount != 20 {
		t.Errorf("expected orphan alert at 20, got %d", defaultDoctorDogOrphanAlertCount)
	}
	if defaultDoctorDogBackupStaleSeconds != 3600.0 {
		t.Errorf("expected backup stale at 3600s, got %.0f", defaultDoctorDogBackupStaleSeconds)
	}
}

func TestDoctorDogThresholds(t *testing.T) {
	// Nil config returns defaults
	lat, orphan, backup := doctorDogThresholds(nil)
	if lat != defaultDoctorDogLatencyAlertMs {
		t.Errorf("expected default latency %.0f, got %.0f", defaultDoctorDogLatencyAlertMs, lat)
	}
	if orphan != defaultDoctorDogOrphanAlertCount {
		t.Errorf("expected default orphan %d, got %d", defaultDoctorDogOrphanAlertCount, orphan)
	}
	if backup != defaultDoctorDogBackupStaleSeconds {
		t.Errorf("expected default backup %.0f, got %.0f", defaultDoctorDogBackupStaleSeconds, backup)
	}

	// Custom config overrides
	config := &DaemonPatrolConfig{
		Patrols: &PatrolsConfig{
			DoctorDog: &DoctorDogConfig{
				Enabled:            true,
				LatencyAlertMs:     3000.0,
				OrphanAlertCount:   10,
				BackupStaleSeconds: 1800.0,
			},
		},
	}
	lat, orphan, backup = doctorDogThresholds(config)
	if lat != 3000.0 {
		t.Errorf("expected custom latency 3000, got %.0f", lat)
	}
	if orphan != 10 {
		t.Errorf("expected custom orphan 10, got %d", orphan)
	}
	if backup != 1800.0 {
		t.Errorf("expected custom backup 1800, got %.0f", backup)
	}

	// Partial override: only latency, rest use defaults
	config.Patrols.DoctorDog = &DoctorDogConfig{
		Enabled:        true,
		LatencyAlertMs: 2000.0,
	}
	lat, orphan, backup = doctorDogThresholds(config)
	if lat != 2000.0 {
		t.Errorf("expected custom latency 2000, got %.0f", lat)
	}
	if orphan != defaultDoctorDogOrphanAlertCount {
		t.Errorf("expected default orphan, got %d", orphan)
	}
	if backup != defaultDoctorDogBackupStaleSeconds {
		t.Errorf("expected default backup, got %.0f", backup)
	}
}

func TestDoctorDogConfigBackwardsCompat(t *testing.T) {
	// Verify that configs with the old max_db_count field can still be parsed
	// (JSON decoder ignores unknown fields by default).
	jsonData := `{"enabled": true, "interval": "3m", "max_db_count": 10}`

	var config DoctorDogConfig
	if err := json.Unmarshal([]byte(jsonData), &config); err != nil {
		t.Fatalf("failed to unmarshal config with old max_db_count field: %v", err)
	}

	if !config.Enabled {
		t.Error("expected enabled=true")
	}
	if config.IntervalStr != "3m" {
		t.Errorf("expected interval=3m, got %s", config.IntervalStr)
	}
}

func TestDoctorDogConfigThresholdFields(t *testing.T) {
	// Verify new threshold fields parse from JSON correctly
	jsonData := `{"enabled": true, "latency_alert_ms": 3000, "orphan_alert_count": 15, "backup_stale_seconds": 1800}`

	var config DoctorDogConfig
	if err := json.Unmarshal([]byte(jsonData), &config); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if config.LatencyAlertMs != 3000.0 {
		t.Errorf("expected latency_alert_ms=3000, got %.0f", config.LatencyAlertMs)
	}
	if config.OrphanAlertCount != 15 {
		t.Errorf("expected orphan_alert_count=15, got %d", config.OrphanAlertCount)
	}
	if config.BackupStaleSeconds != 1800.0 {
		t.Errorf("expected backup_stale_seconds=1800, got %.0f", config.BackupStaleSeconds)
	}
}

func writeDoctorDogMetadata(t *testing.T, beadsDir, db string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"backend":       "dolt",
		"database":      "dolt",
		"dolt_mode":     "server",
		"dolt_database": db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDoctorDogRigs(t *testing.T, townRoot string, prefixes map[string]string) {
	t.Helper()
	rigs := make(map[string]any, len(prefixes))
	for name, prefix := range prefixes {
		rigs[name] = map[string]any{
			"git_url": "https://example.com/" + name + ".git",
			"beads":   map[string]string{"prefix": prefix},
		}
	}
	data, err := json.Marshal(map[string]any{
		"version": 1,
		"rigs":    rigs,
	})
	if err != nil {
		t.Fatal(err)
	}
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	if err := os.MkdirAll(filepath.Dir(rigsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rigsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
