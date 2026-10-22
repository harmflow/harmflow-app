package schema

import (
	"strings"
	"testing"
)

const valid = `apiVersion: harmflow/v1alpha1
kind: Task
metadata:
  id: weekly-notes
  name: Weekly notes
spec:
  type: general
  prompt: Summarize the week.
  harness:
    preferred:
      provider: claude-code
`

func TestValidTask(t *testing.T) {
	v, err := New()
	if err != nil {
		t.Fatal(err)
	}
	doc, node, issues := ParseYAML([]byte(valid))
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	if got := v.ValidateTask(doc, node); len(got) > 0 {
		t.Fatalf("valid task rejected: %+v", got)
	}
}

// FS-001 AC-009: a syntax error names the line.
func TestSyntaxErrorLine(t *testing.T) {
	_, _, issues := ParseYAML([]byte("apiVersion: harmflow/v1alpha1\nkind: Task\nmetadata:\n  id: [unclosed\n"))
	if len(issues) != 1 || issues[0].Line == 0 || issues[0].Code != "syntax" {
		t.Fatalf("got %+v", issues)
	}
}

// FS-001 AC-010, AC-013: a general task takes no repository; a GitHub task needs them; a schema error is located.
func TestTypeParametersAndLocation(t *testing.T) {
	v, _ := New()
	gh := strings.Replace(valid, "type: general", "type: github-issue-implementation", 1)
	doc, node, _ := ParseYAML([]byte(gh))
	issues := v.ValidateTask(doc, node)
	if len(issues) == 0 || !strings.Contains(issues[0].Message, "githubIssueImplementation") {
		t.Fatalf("a GitHub task without repositories passed: %+v", issues)
	}
	withRepo := valid + "  githubIssueImplementation:\n    repositories:\n      - name: o/r\n        localPath: /x\n"
	doc, node, _ = ParseYAML([]byte(withRepo))
	if issues := v.ValidateTask(doc, node); len(issues) == 0 {
		t.Fatal("a general task accepted repositories")
	}
	bad := strings.Replace(valid, "provider: claude-code", "provider: claude-code\n      effort: 3", 1)
	doc, node, _ = ParseYAML([]byte(bad))
	issues = v.ValidateTask(doc, node)
	if len(issues) == 0 || issues[0].Line != 12 || issues[0].Path != "/spec/harness/preferred/effort" {
		t.Fatalf("got %+v, want the effort on line 12", issues)
	}
}

// FS-013 FR-002: the rules of a type apply only to a task that names it, so a task with no type is told its type is
// missing, and not that its prompt and harness are not allowed.
func TestTypeRulesNeedTheType(t *testing.T) {
	v, _ := New()
	doc, node, _ := ParseYAML([]byte(strings.Replace(valid, "  type: general\n", "", 1)))
	issues := v.ValidateTask(doc, node)
	missing := false
	for _, is := range issues {
		if is.Key == "schema_false" {
			t.Errorf("a task with no type got %s at %s", is.Message, is.Path)
		}
		missing = missing || (is.Key == "schema_required" && strings.Contains(is.Message, `"type"`))
	}
	if !missing {
		t.Errorf("issues %+v", issues)
	}
}

// FS-001 AC-022: notifications cite the schema's event names.
func TestNotificationEventNames(t *testing.T) {
	v, _ := New()
	bad := valid + "  notifications:\n    native:\n      events: [needs-attention]\n"
	doc, node, _ := ParseYAML([]byte(bad))
	if issues := v.ValidateTask(doc, node); len(issues) == 0 {
		t.Fatal("needs-attention accepted")
	}
	ok := valid + "  notifications:\n    native:\n      events: [execution.needs_attention]\n"
	doc, node, _ = ParseYAML([]byte(ok))
	if issues := v.ValidateTask(doc, node); len(issues) > 0 {
		t.Fatalf("schema name rejected: %+v", issues)
	}
}

func TestSchedulesAreExclusive(t *testing.T) {
	v, _ := New()
	both := valid + "  schedule:\n    cron: \"0 9 * * *\"\n    interval: 30m\n"
	doc, node, _ := ParseYAML([]byte(both))
	if issues := v.ValidateTask(doc, node); len(issues) == 0 {
		t.Fatal("cron and interval together were accepted")
	}
}

// FS-012 FR-005: a schema error carries a key and params the desktop translates, beside its en-US text.
func TestSchemaErrorsAreTranslatable(t *testing.T) {
	v, _ := New()
	cases := []struct {
		yaml, key, param, want string
	}{
		{valid + "  colour: red\n", "schema_additional_properties", "properties", `"colour"`},
		{strings.Replace(valid, "  prompt: Summarize the week.\n", "", 1), "schema_required", "properties", `"prompt"`},
		{strings.Replace(valid, "id: weekly-notes", "id: Weekly_Notes", 1), "schema_pattern", "value", `"Weekly_Notes"`},
		{valid + "  enabled: maybe\n", "schema_type", "got", "string"},
	}
	for _, c := range cases {
		doc, node, _ := ParseYAML([]byte(c.yaml))
		issues := v.ValidateTask(doc, node)
		if len(issues) == 0 {
			t.Errorf("%s: no issue", c.key)
			continue
		}
		got := issues[0]
		if got.Key != c.key || got.Params[c.param] != c.want || !strings.Contains(got.Message, c.want) {
			t.Errorf("%s: got %+v", c.key, got)
		}
	}
	_, _, issues := ParseYAML([]byte("kind: [unclosed\n"))
	if len(issues) != 1 || issues[0].Key != "yaml_syntax" || issues[0].Params["detail"] == "" {
		t.Errorf("syntax error: %+v", issues)
	}
}

func TestReportSchema(t *testing.T) {
	v, _ := New()
	good, _ := FromJSON([]byte(`{"outcome":"no_work","summary":"nothing eligible"}`))
	if issues := v.ValidateReport(good); len(issues) > 0 {
		t.Fatal(issues)
	}
	bad, _ := FromJSON([]byte(`{"outcome":"done"}`))
	if issues := v.ValidateReport(bad); len(issues) == 0 {
		t.Fatal("unknown outcome accepted")
	}
}
