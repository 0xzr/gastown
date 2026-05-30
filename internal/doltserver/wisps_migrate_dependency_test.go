package doltserver

import (
	"strings"
	"testing"
)

func TestBuildWispDependenciesCopyQueryUsesSplitTargetSchema(t *testing.T) {
	query := buildWispDependenciesCopyQuery(true)

	if !strings.Contains(query, "COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external)") {
		t.Fatalf("split-schema query did not use dependency target COALESCE: %s", query)
	}
	if strings.Contains(query, "d.depends_on_id") {
		t.Fatalf("split-schema query still selects legacy target column: %s", query)
	}
}

func TestBuildWispDependenciesCopyQueryUsesLegacyTargetSchema(t *testing.T) {
	query := buildWispDependenciesCopyQuery(false)

	if !strings.Contains(query, "SELECT d.issue_id, d.depends_on_id, d.type") {
		t.Fatalf("legacy query did not select depends_on_id: %s", query)
	}
}
