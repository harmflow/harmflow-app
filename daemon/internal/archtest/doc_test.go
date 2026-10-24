package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestExportedNamesAreDocumented checks that every exported name of the daemon's production files outside
// internal/contracts has a doc comment (D-132, harmflow/harmflow-app#198). Each failure names the file, the line, and
// the name.
func TestExportedNamesAreDocumented(t *testing.T) {
	missing, err := undocumented(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range missing {
		t.Error(m)
	}
}

// undocumented parses every production .go file under root/internal and root/cmd, root being the module directory,
// except those of internal/contracts, and returns each exported name with no doc comment as "file:line: Name", file
// relative to root. The names counted are functions, methods on exported types, types, and each name of a var or const
// spec; a comment on the declaration, on its group, on the spec, or at the end of the spec's line documents a name.
func undocumented(root string) ([]string, error) {
	var missing []string
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				// The go command ignores these directories too; the contracts are generated from the schemas.
				if name == "testdata" || name != top && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) ||
					rel == "internal/contracts" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			report := func(pos token.Pos, name string) {
				missing = append(missing, fmt.Sprintf("%s:%d: %s", rel, fset.Position(pos).Line, name))
			}
			for _, decl := range f.Decls {
				switch decl := decl.(type) {
				case *ast.FuncDecl:
					if decl.Name.IsExported() && decl.Doc == nil && (decl.Recv == nil || exportedReceiver(decl.Recv)) {
						report(decl.Pos(), decl.Name.Name)
					}
				case *ast.GenDecl:
					if decl.Tok == token.IMPORT || decl.Doc != nil {
						continue
					}
					for _, spec := range decl.Specs {
						switch spec := spec.(type) {
						case *ast.TypeSpec:
							if spec.Name.IsExported() && spec.Doc == nil && spec.Comment == nil {
								report(spec.Pos(), spec.Name.Name)
							}
						case *ast.ValueSpec:
							for _, n := range spec.Names {
								if n.IsExported() && spec.Doc == nil && spec.Comment == nil {
									report(n.Pos(), n.Name)
								}
							}
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return missing, nil
}

// exportedReceiver reports whether a method's receiver type is exported, through a pointer and type parameters.
func exportedReceiver(recv *ast.FieldList) bool {
	t := recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch generic := t.(type) {
	case *ast.IndexExpr:
		t = generic.X
	case *ast.IndexListExpr:
		t = generic.X
	}
	id, ok := t.(*ast.Ident)
	return ok && id.IsExported()
}
