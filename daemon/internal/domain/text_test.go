package domain

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTextRendersItsTemplate(t *testing.T) {
	got := T("cron_value_invalid", `invalid {{fieldName}} field "{{value}}"`, "fieldName", "hour", "value", "{{x}}")
	if got.Message != `invalid hour field "{{x}}"` || got.Params["value"] != "{{x}}" || got.Key != "cron_value_invalid" {
		t.Errorf("got %+v", got)
	}
	wrapped := fmt.Errorf("saving: %w", TextErr("task_not_found", "no task {{task}}", "task", "t"))
	if text := TextOf(wrapped); text.Key != "task_not_found" || text.Message != "no task t" {
		t.Errorf("TextOf a wrapped TextError: %+v", text)
	}
	if text := TextOf(errors.New("exit status 1")); text.Key != "" || text.Message != "exit status 1" {
		t.Errorf("TextOf a plain error: %+v", text)
	}
}

// FS-012 FR-005: every key the daemon writes is one the contract declares, so the desktop has a text for it, and the
// contract declares no key the daemon never writes.
func TestMessageKeysMatchTheContract(t *testing.T) {
	used := keysInSource(t, "..")
	data, err := os.ReadFile("../../../contracts/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas struct {
				MessageKey struct {
					Enum []string `yaml:"enum"`
				} `yaml:"MessageKey"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, k := range doc.Components.Schemas.MessageKey.Enum {
		declared[k] = true
	}
	var undeclared, unused []string
	for k := range used {
		if !declared[k] {
			undeclared = append(undeclared, k)
		}
	}
	for k := range declared {
		if !used[k] {
			unused = append(unused, k)
		}
	}
	sort.Strings(undeclared)
	sort.Strings(unused)
	if len(undeclared) > 0 || len(unused) > 0 {
		t.Errorf("MessageKey in contracts/api/openapi.yaml: missing %v; never written %v", undeclared, unused)
	}
}

// keysInSource collects the literal key of every T and TextErr call in the daemon's non-test code.
func keysInSource(t *testing.T, root string) map[string]bool {
	keys := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if filepath.Base(filepath.Dir(path)) == "domain" && filepath.Base(path) == "text.go" {
			return nil // T and TextErr themselves pass a key along
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			var name string
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if file.Name.Name == "domain" {
					name = fn.Name
				}
			case *ast.SelectorExpr:
				if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "domain" {
					name = fn.Sel.Name
				}
			}
			if name != "T" && name != "TextErr" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: the key of %s must be a literal", fset.Position(call.Pos()), name)
				return true
			}
			key, _ := strconv.Unquote(lit.Value)
			keys[key] = true
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return keys
}
