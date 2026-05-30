package beads

import (
	"errors"
	"testing"
)

func TestDependencyTargetExpr(t *testing.T) {
	tests := []struct {
		name        string
		tableAlias  string
		splitTarget bool
		want        string
	}{
		{
			name:        "legacy unqualified",
			splitTarget: false,
			want:        "depends_on_id",
		},
		{
			name:        "legacy qualified",
			tableAlias:  "d",
			splitTarget: false,
			want:        "d.depends_on_id",
		},
		{
			name:        "split qualified",
			tableAlias:  "d",
			splitTarget: true,
			want:        "COALESCE(d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DependencyTargetExpr(tt.tableAlias, tt.splitTarget); got != tt.want {
				t.Fatalf("DependencyTargetExpr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDependencyTargetSelectExprAliasesSplitTarget(t *testing.T) {
	got := DependencyTargetSelectExpr("", true)
	want := "COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id"
	if got != want {
		t.Fatalf("DependencyTargetSelectExpr() = %q, want %q", got, want)
	}
}

func TestDependencyTargetMatchExpr(t *testing.T) {
	got := DependencyTargetMatchExpr("d", "'gt-target'", true)
	want := "'gt-target' IN (d.depends_on_issue_id, d.depends_on_wisp_id, d.depends_on_external)"
	if got != want {
		t.Fatalf("DependencyTargetMatchExpr() = %q, want %q", got, want)
	}
}

func TestIsDependencyTargetColumnError(t *testing.T) {
	err := errors.New(`query error: column "depends_on_id" could not be found in any table in scope`)
	if !IsDependencyTargetColumnError(err) {
		t.Fatal("expected depends_on_id missing-column error to be detected")
	}

	if IsDependencyTargetColumnError(errors.New("syntax error near dependencies")) {
		t.Fatal("unexpected dependency target column match for unrelated error")
	}
}
