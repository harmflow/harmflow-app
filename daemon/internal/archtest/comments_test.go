package archtest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// commentWidth is the column a Go comment wraps at, as docs/go-code-guide.md sets it. A tab counts as one column.
const commentWidth = 120

// TestCommentsWrapAt120Columns checks every Go file of the repository's Go modules, test files included, for a comment
// line wider than commentWidth (harmflow/harmflow-app#523). Each failure names the file and the line.
func TestCommentsWrapAt120Columns(t *testing.T) {
	long, err := longComments(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range long {
		t.Errorf("%s:%d: the comment is %d columns wide; wrap it at %d", l.file, l.line, l.width, commentWidth)
	}
}

// TestLongCommentFixtures runs the check over a planted tree: a comment wider than the limit fails, one at the limit
// passes, and a token no rewrap can bring within it is exempt.
func TestLongCommentFixtures(t *testing.T) {
	root := t.TempDir()
	x := func(n int) string { return strings.Repeat("x", n) }
	// prose is n columns of words, which a rewrap can move to the next line.
	prose := func(n int) string { return strings.Repeat("word ", n/5+1)[:n] }
	files := map[string]string{
		"m/go.mod": "module example.com/m\n",
		"m/m.go": strings.Join([]string{
			"package m",
			"",
			"// " + prose(117),   // 3: 120 columns, at the limit
			"// " + prose(118),   // 4: 121 columns
			"\t// " + prose(116), // 5: 120 columns, the tab counted as one
			"\t// " + prose(117), // 6: 121 columns, the tab counted as one
			// 7: a word across column 120 moves to the next line, so it is no exemption
			"// " + strings.Repeat("word ", 23) + "straddling",
			// 8: a URL wider than a comment line of its own cannot be wrapped
			"// see https://example.com/" + x(100),
			// 9: a URL that fits on a comment line of its own can
			"// " + strings.Repeat("word ", 10) + "https://example.com/" + x(60),
			`var v = "` + x(130) + `" // a code line has no limit`,
			"",
		}, "\n"),
		"m/m_test.go":       "package m\n\n// " + prose(118) + "\n",
		"m/sub/sub.go":      "package sub\n\n// " + prose(118) + "\n",
		"m/testdata/t.go":   "package t\n\n// " + prose(118) + "\n",
		"m/.hidden/h.go":    "package h\n\n// " + prose(118) + "\n",
		"m/README.md":       "// " + prose(118) + "\n",
		"notamodule/n.go":   "package n\n\n// " + prose(118) + "\n",
		"notamodule/go.txt": "",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	long, err := longComments(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range long {
		got = append(got, fmt.Sprintf("%s:%d:%d", l.file, l.line, l.width))
	}
	want := []string{"m/m.go:4:121", "m/m.go:6:121", "m/m.go:7:128", "m/m.go:9:133", "m/m_test.go:3:121",
		"m/sub/sub.go:3:121"}
	if !slices.Equal(got, want) {
		t.Errorf("long comments:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// longComment is a comment line wider than commentWidth; file is relative to the repository root.
type longComment struct {
	file        string
	line, width int
}

// longComments reads every .go file of each Go module at the top of root, a folder holding a go.mod, and returns the
// comment lines wider than commentWidth that a rewrap can bring within it, sorted by file and line. A comment line is
// one that starts with "//" after its indenting tabs, as the issue's search counts them; a comment after code is not.
// It skips the folders the go command ignores: testdata, and those whose name starts with "." or "_".
func longComments(root string) ([]longComment, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var long []longComment
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "go.mod")); err != nil {
			continue
		}
		top := filepath.Join(root, e.Name())
		err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if path != top && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(name, ".go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSuffix(line, "\r")
				width := utf8.RuneCountInString(line)
				if width <= commentWidth || !strings.HasPrefix(strings.TrimLeft(line, "\t"), "//") || unwrappable(line) {
					continue
				}
				long = append(long, longComment{file: filepath.ToSlash(rel), line: i + 1, width: width})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(long, func(a, b longComment) int {
		if c := strings.Compare(a.file, b.file); c != 0 {
			return c
		}
		return a.line - b.line
	})
	return long, nil
}

// unwrappable reports whether a comment line wider than commentWidth overflows by a single token that no rewrap can
// bring within it, such as a long URL: no space follows column commentWidth, and the token that crosses it, alone on a
// comment line at the same indentation, would still be wider.
func unwrappable(line string) bool {
	if strings.Contains(string([]rune(line)[commentWidth:]), " ") {
		return false
	}
	token := line[strings.LastIndexByte(line, ' ')+1:]
	indent := len(line) - len(strings.TrimLeft(line, "\t"))
	return indent+len("// ")+utf8.RuneCountInString(token) > commentWidth
}
