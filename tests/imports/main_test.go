package main

import (
	"strings"
	"testing"
)

// The fixtures are strings, not files: a Go file under testdata would be read by gofmt -l and by this check in CI.
func TestCheck(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // a part of the one fault reported; empty for none
	}{
		{"three groups in order", `package p

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)
`, ""},
		{"standard library only, with a side-effect import", `package p

import (
	_ "embed"
	"fmt"
)
`, ""},
		{"one import declaration per group", `package p

import "fmt"

import "github.com/harmflow/harmflow-app/daemon/internal/domain"
`, ""},
		{"module package in the third-party group", `package p

import (
	"fmt"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"gopkg.in/yaml.v3"
)
`, `"gopkg.in/yaml.v3" is a third-party import in the module group`},
		{"third-party package in the standard-library group", `package p

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)
`, `"golang.org/x/sys/unix" is a third-party import in the standard-library group`},
		{"groups in another order", `package p

import (
	"fmt"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"

	"gopkg.in/yaml.v3"
)
`, "the third-party group comes after the module group"},
		{"no blank line between groups", `package p

import (
	"fmt"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)
`, `"github.com/harmflow/harmflow-app/daemon/internal/domain" is a module import in the standard-library group`},
		{"a group split in two", `package p

import (
	"fmt"

	"os"
)
`, "a second standard-library group"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			faults, err := check("p.go", []byte(c.src))
			if err != nil {
				t.Fatalf("check(%q) error = %v", c.name, err)
			}
			switch {
			case c.want == "" && len(faults) > 0:
				t.Errorf("check() = %q, want no fault", faults)
			case c.want != "" && (len(faults) != 1 || !strings.Contains(faults[0], c.want)):
				t.Errorf("check() = %q, want one fault saying %q", faults, c.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		path string
		want group
	}{
		{"fmt", standard},
		{"net/http", standard},
		{"golang.org/x/sys/unix", thirdParty},
		{"gopkg.in/yaml.v3", thirdParty},
		{"github.com/harmflow/harmflow-app-extra/x", thirdParty},
		{"github.com/harmflow/harmflow-app/daemon/internal/app", module},
		{"github.com/harmflow/harmflow-app/tests/integration", module},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := classify(c.path); got != c.want {
				t.Errorf("classify(%q) = %s, want %s", c.path, groupNames[got], groupNames[c.want])
			}
		})
	}
}
