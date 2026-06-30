package beads

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestNoAdHocBdSubprocessesInHardenedPackages(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	packages := []string{
		"internal/deacon",
		"internal/plugin",
		"internal/refinery",
		"internal/witness",
	}

	var violations []string
	for _, pkg := range packages {
		dir := filepath.Join(repoRoot, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			violations = append(violations, adHocBDSubprocesses(t, repoRoot, path)...)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("do not spawn bd directly in hardened packages; use internal/beads.Command so env targeting, read-only mode, and side-effect suppression stay centralized:\n%s", strings.Join(violations, "\n"))
	}
}

func adHocBDSubprocesses(t *testing.T, repoRoot, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isExecCommandCall(call, file) {
			return true
		}
		argIndex := 0
		if selectorName(call) == "CommandContext" {
			argIndex = 1
		}
		if len(call.Args) <= argIndex || !isBDCommandArg(call.Args[argIndex]) {
			return true
		}
		pos := fset.Position(call.Pos())
		rel, err := filepath.Rel(repoRoot, pos.Filename)
		if err != nil {
			rel = pos.Filename
		}
		out = append(out, rel+":"+strconv.Itoa(pos.Line))
		return true
	})
	return out
}

func isExecCommandCall(call *ast.CallExpr, file *ast.File) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return importPathForIdent(file, x.Name) == "os/exec"
}

func importPathForIdent(file *ast.File, name string) string {
	for _, imp := range file.Imports {
		local := ""
		if imp.Name != nil {
			local = imp.Name.Name
			if local == "_" || local == "." {
				continue
			}
		} else {
			pathStr, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			local = path.Base(pathStr)
		}
		if local == name {
			pathStr, _ := strconv.Unquote(imp.Path.Value)
			return pathStr
		}
	}
	return ""
}

func selectorName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

func isBDCommandArg(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return false
		}
		value, err := strconv.Unquote(v.Value)
		return err == nil && value == "bd"
	case *ast.Ident:
		return strings.EqualFold(v.Name, "bdPath")
	case *ast.SelectorExpr:
		return strings.EqualFold(v.Sel.Name, "bdPath")
	default:
		return false
	}
}

func TestAdHocBDSubprocessDetectsAliasedExecImport(t *testing.T) {
	repoRoot := t.TempDir()
	srcDir := filepath.Join(repoRoot, "internal", "witness")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(srcDir, "aliased_exec.go")
	content := `package witness

import (
	"context"
	stdexec "os/exec"
)

func runAliasedBD() {
	_ = stdexec.CommandContext(context.Background(), "bd", "list")
}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	violations := adHocBDSubprocesses(t, repoRoot, path)
	if len(violations) == 0 {
		t.Fatalf("expected a violation for aliased os/exec import, got none")
	}
}
