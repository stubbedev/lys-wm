package testenv

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testhomePath = "github.com/stubbedev/lys-lib/testhome"

type testPackage struct {
	testMains  []string
	isolated   bool
	guarded    bool
	firstError error
}

func TestEveryTestBinaryRunsInATestHome(t *testing.T) {
	root := moduleRoot(t)
	packages := map[string]*testPackage{}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return skipDir(root, path, d.Name())
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		pkg, seen := packages[dir]
		if !seen {
			pkg = &testPackage{}
			packages[dir] = pkg
			dirs = append(dirs, dir)
		}
		pkg.read(path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module at %s: %v", root, err)
	}
	if len(dirs) == 0 {
		t.Fatalf("found no test files under %s", root)
	}
	for _, dir := range dirs {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("relate %s to %s: %v", dir, root, err)
		}
		pkg := packages[dir]
		switch {
		case pkg.firstError != nil:
			t.Errorf("%s: %v", rel, pkg.firstError)
		case len(pkg.testMains) != 1:
			t.Errorf("%s: %d TestMains %v, want one that calls testhome.Run", rel, len(pkg.testMains), pkg.testMains)
		case !pkg.isolated:
			t.Errorf("%s: TestMain in %s does not call testhome.Run or testhome.Main, so the test binary reaches the host's system and session bus", rel, pkg.testMains[0])
		case !pkg.guarded:
			t.Errorf("%s: no test calls testhome.RequireNoHostSession", rel)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		_, err := os.Stat(filepath.Join(dir, "go.mod"))
		if err == nil {
			return dir
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("look for go.mod in %s: %v", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

func skipDir(root, path, name string) error {
	if path == root {
		return nil
	}
	if name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return filepath.SkipDir
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	if err == nil {
		return filepath.SkipDir
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (p *testPackage) read(path string) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		if p.firstError == nil {
			p.firstError = err
		}
		return
	}
	name, imported := testhomeName(file)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		if fn.Name.Name == "TestMain" {
			p.testMains = append(p.testMains, filepath.Base(path))
			p.isolated = p.isolated || imported && calls(fn.Body, name, "Run", "Main")
		}
		p.guarded = p.guarded || imported && calls(fn.Body, name, "RequireNoHostSession")
	}
}

func testhomeName(file *ast.File) (string, bool) {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != testhomePath {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name, true
		}
		return filepath.Base(testhomePath), true
	}
	return "", false
}

func calls(body *ast.BlockStmt, pkg string, funcs ...string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return !found
		}
		if x, ok := sel.X.(*ast.Ident); ok && x.Name == pkg {
			for _, f := range funcs {
				found = found || sel.Sel.Name == f
			}
		}
		return !found
	})
	return found
}
