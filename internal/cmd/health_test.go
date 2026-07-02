package cmd

import (
	"reflect"
	"testing"
)

func TestBuildHealthReportUsesConfiguredProductionDatabases(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()
	wantDBs := []string{"hq", "gastown", "polybot"}
	var databaseDBs []string
	var pollutionDBs []string

	report := buildHealthReport(townRoot, healthReportDeps{
		checkServerHealth: func(root string) *ServerHealth {
			if root != townRoot {
				t.Fatalf("checkServerHealth root = %q, want %q", root, townRoot)
			}
			return &ServerHealth{Running: true, Port: 3307}
		},
		productionDatabases: func(root string) []string {
			if root != townRoot {
				t.Fatalf("productionDatabases root = %q, want %q", root, townRoot)
			}
			return append([]string(nil), wantDBs...)
		},
		checkDatabaseHealth: func(port int, dbs []string) []DatabaseHealth {
			if port != 3307 {
				t.Fatalf("checkDatabaseHealth port = %d, want 3307", port)
			}
			databaseDBs = append([]string(nil), dbs...)
			return []DatabaseHealth{{Name: dbs[0]}}
		},
		checkPollution: func(port int, dbs []string) []PollutionRecord {
			if port != 3307 {
				t.Fatalf("checkPollution port = %d, want 3307", port)
			}
			pollutionDBs = append([]string(nil), dbs...)
			return nil
		},
		checkBackupHealth:  func(string) *BackupHealth { return &BackupHealth{} },
		checkProcessHealth: func(int) *ProcessHealth { return &ProcessHealth{} },
		checkOrphanDatabases: func(string) []OrphanDB {
			return []OrphanDB{{Name: "gtviz"}}
		},
	})

	if !reflect.DeepEqual(databaseDBs, wantDBs) {
		t.Fatalf("database health DBs = %#v, want %#v", databaseDBs, wantDBs)
	}
	if !reflect.DeepEqual(pollutionDBs, wantDBs) {
		t.Fatalf("pollution DBs = %#v, want %#v", pollutionDBs, wantDBs)
	}
	if len(report.Orphans) != 1 || report.Orphans[0].Name != "gtviz" {
		t.Fatalf("orphans = %#v, want gtviz reported separately", report.Orphans)
	}
}
