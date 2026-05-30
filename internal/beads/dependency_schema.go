package beads

import "strings"

const DependencyTargetAlias = "depends_on_id"

var splitDependencyTargetColumns = []string{
	"depends_on_issue_id",
	"depends_on_wisp_id",
	"depends_on_external",
}

// DependencyTargetExpr returns the SQL expression for a dependency target.
// When splitTarget is true, bd stores targets in type-specific columns and the
// caller should treat the first non-null value as the legacy depends_on_id.
func DependencyTargetExpr(tableAlias string, splitTarget bool) string {
	if !splitTarget {
		return qualifyDependencyColumn(tableAlias, DependencyTargetAlias)
	}

	columns := make([]string, 0, len(splitDependencyTargetColumns))
	for _, column := range splitDependencyTargetColumns {
		columns = append(columns, qualifyDependencyColumn(tableAlias, column))
	}
	return "COALESCE(" + strings.Join(columns, ", ") + ")"
}

// DependencyTargetSelectExpr returns a SELECT expression whose result column is
// named depends_on_id for both legacy and split dependency-target schemas.
func DependencyTargetSelectExpr(tableAlias string, splitTarget bool) string {
	expr := DependencyTargetExpr(tableAlias, splitTarget)
	if !splitTarget {
		return expr
	}
	return expr + " AS " + DependencyTargetAlias
}

// DependencyTargetMatchExpr returns a WHERE predicate for a quoted SQL value.
// The quotedValue argument must already be a safe SQL string literal.
func DependencyTargetMatchExpr(tableAlias, quotedValue string, splitTarget bool) string {
	if !splitTarget {
		return DependencyTargetExpr(tableAlias, false) + " = " + quotedValue
	}

	columns := make([]string, 0, len(splitDependencyTargetColumns))
	for _, column := range splitDependencyTargetColumns {
		columns = append(columns, qualifyDependencyColumn(tableAlias, column))
	}
	return quotedValue + " IN (" + strings.Join(columns, ", ") + ")"
}

// IsDependencyTargetColumnError reports whether err came from a dependency
// target-column schema mismatch between legacy and split bd schemas.
func IsDependencyTargetColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	missingColumn := strings.Contains(msg, "unknown column") ||
		strings.Contains(msg, "could not be found") ||
		strings.Contains(msg, "no such column")
	if !missingColumn {
		return false
	}
	if strings.Contains(msg, DependencyTargetAlias) {
		return true
	}
	for _, column := range splitDependencyTargetColumns {
		if strings.Contains(msg, column) {
			return true
		}
	}
	return false
}

func qualifyDependencyColumn(tableAlias, column string) string {
	if tableAlias == "" {
		return column
	}
	return tableAlias + "." + column
}
