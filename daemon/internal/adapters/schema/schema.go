// Package schema validates task documents, execution reports, and events against the embedded copies of
// contracts/schemas, and locates each problem in the YAML by line and column (FS-001 FR-007, FR-011).
package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"

	"github.com/harmflow/harmflow-app/daemon/internal/contracts"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Issue is one validation problem, located when possible.
type Issue struct {
	Path string `json:"path,omitempty"`
	domain.Text
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	Code   string `json:"code,omitempty"`
}

type Validator struct {
	task   *jsonschema.Schema
	report *jsonschema.Schema
	event  *jsonschema.Schema
}

func New() (*Validator, error) {
	c := jsonschema.NewCompiler()
	// An event's timestamp is checked as a date-time; the task and report schemas name no format.
	c.AssertFormat()
	for url, raw := range map[string][]byte{
		contracts.TaskSchemaURL:   contracts.TaskSchema,
		contracts.ReportSchemaURL: contracts.ExecutionReportSchema,
		contracts.EventSchemaURL:  contracts.EventSchema,
	} {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("schema %s: %w", url, err)
		}
		if err := c.AddResource(url, doc); err != nil {
			return nil, err
		}
	}
	task, err := c.Compile(contracts.TaskSchemaURL)
	if err != nil {
		return nil, err
	}
	report, err := c.Compile(contracts.ReportSchemaURL)
	if err != nil {
		return nil, err
	}
	event, err := c.Compile(contracts.EventSchemaURL)
	if err != nil {
		return nil, err
	}
	return &Validator{task: task, report: report, event: event}, nil
}

var yamlLine = regexp.MustCompile(`line (\d+)(?::|,)?\s*(?:column (\d+))?`)

// ParseYAML decodes YAML text into a JSON-compatible value and its node tree. A syntax error is reported with its
// line, and its column when the parser gives one.
func ParseYAML(text []byte) (any, *yaml.Node, []Issue) {
	var node yaml.Node
	if err := yaml.Unmarshal(text, &node); err != nil {
		return nil, nil, []Issue{syntaxIssue(err)}
	}
	if len(node.Content) == 0 {
		return nil, &node, []Issue{{Text: domain.T("document_empty", "the document is empty"), Code: "syntax"}}
	}
	var raw any
	if err := node.Decode(&raw); err != nil {
		return nil, &node, []Issue{syntaxIssue(err)}
	}
	value, err := jsonCompatible(raw)
	if err != nil {
		return nil, &node, []Issue{{Text: domain.TextOf(err), Code: "syntax"}}
	}
	return value, &node, nil
}

func syntaxIssue(err error) Issue {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	is := Issue{Text: domain.T("yaml_syntax", "YAML syntax error: {{detail}}", "detail", msg), Code: "syntax"}
	if m := yamlLine.FindStringSubmatch(msg); m != nil {
		is.Line, _ = strconv.Atoi(m[1])
		if m[2] != "" {
			is.Column, _ = strconv.Atoi(m[2])
		}
	}
	return is
}

// jsonCompatible round-trips through JSON so the validator sees json.Number and map[string]any only.
func jsonCompatible(v any) (any, error) {
	data, err := json.Marshal(normalize(v))
	if err != nil {
		return nil, domain.TextErr("document_not_representable", "the document cannot be represented as JSON: {{detail}}",
			"detail", err.Error())
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[fmt.Sprint(k)] = normalize(x)
		}
		return m
	case []any:
		for i := range t {
			t[i] = normalize(t[i])
		}
		return t
	}
	return v
}

// FromJSON prepares a document received as JSON (the form) for validation.
func FromJSON(data []byte) (any, error) {
	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

// ValidateTask checks a decoded document against task.schema.json; node, when given, locates each issue.
func (v *Validator) ValidateTask(doc any, node *yaml.Node) []Issue {
	return collect(v.task.Validate(doc), node)
}

// ValidateReport checks report.json against execution-report.schema.json.
func (v *Validator) ValidateReport(doc any) []Issue {
	return collect(v.report.Validate(doc), nil)
}

// ValidateEvent checks an event, decoded from the JSON its log line or SSE frame carries, against event.schema.json
// (FS-008 FR-007).
func (v *Validator) ValidateEvent(doc any) []Issue {
	return collect(v.event.Validate(doc), nil)
}

var printer = message.NewPrinter(language.English)

func collect(err error, node *yaml.Node) []Issue {
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Issue{{Text: domain.Text{Message: err.Error()}, Code: "schema"}}
	}
	var leaves []*jsonschema.ValidationError
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, e)
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	seen := map[string]bool{}
	var out []Issue
	for _, l := range leaves {
		path := "/" + strings.Join(l.InstanceLocation, "/")
		if len(l.InstanceLocation) == 0 {
			path = ""
		}
		text := kindText(l.ErrorKind)
		key := path + "|" + text.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		is := Issue{Path: path, Text: text, Code: "schema"}
		if node != nil {
			is.Line, is.Column = Locate(node, l.InstanceLocation)
		}
		out = append(out, is)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// kindText is the translatable text of a schema error, for the kinds the task and report schemas raise; any other
// kind keeps the validator's own en-US text, with no key.
func kindText(k jsonschema.ErrorKind) domain.Text {
	switch k := k.(type) {
	case *kind.Type:
		return domain.T("schema_type", "expected {{want}}, got {{got}}", "want", strings.Join(k.Want, "/"), "got", k.Got)
	case *kind.Enum:
		if values, ok := primitives(k.Want); ok {
			if len(values) == 1 {
				return domain.T("schema_const", "value must be {{value}}", "value", values[0])
			}
			return domain.T("schema_enum", "value must be one of {{values}}", "values", strings.Join(values, ", "))
		}
	case *kind.Const:
		if values, ok := primitives([]any{k.Want}); ok {
			return domain.T("schema_const", "value must be {{value}}", "value", values[0])
		}
	case *kind.Required:
		return domain.T("schema_required",
			plural(len(k.Missing), "missing property {{properties}}", "missing properties {{properties}}"),
			"properties", quoteAll(k.Missing), "count", strconv.Itoa(len(k.Missing)))
	case *kind.AdditionalProperties:
		return domain.T("schema_additional_properties",
			plural(len(k.Properties), "property {{properties}} is not allowed", "properties {{properties}} are not allowed"),
			"properties", quoteAll(k.Properties), "count", strconv.Itoa(len(k.Properties)))
	case *kind.Pattern:
		return domain.T("schema_pattern", "{{value}} does not match the pattern {{pattern}}",
			"value", strconv.Quote(k.Got), "pattern", strconv.Quote(k.Want))
	case *kind.MinLength:
		return domain.T("schema_min_length",
			plural(k.Want, "must have at least {{count}} character", "must have at least {{count}} characters"),
			"count", strconv.Itoa(k.Want))
	case *kind.MaxLength:
		return domain.T("schema_max_length",
			plural(k.Want, "must have at most {{count}} character", "must have at most {{count}} characters"),
			"count", strconv.Itoa(k.Want))
	case *kind.Minimum:
		return domain.T("schema_minimum", "must be at least {{limit}}", "limit", k.Want.RatString())
	case *kind.Maximum:
		return domain.T("schema_maximum", "must be at most {{limit}}", "limit", k.Want.RatString())
	case *kind.MinItems:
		return domain.T("schema_min_items",
			plural(k.Want, "must have at least {{count}} item", "must have at least {{count}} items"),
			"count", strconv.Itoa(k.Want))
	case *kind.UniqueItems:
		return domain.T("schema_unique_items", "items at {{first}} and {{second}} are equal",
			"first", strconv.Itoa(k.Duplicates[0]), "second", strconv.Itoa(k.Duplicates[1]))
	case *kind.Not:
		return domain.T("schema_not", "this combination of properties is not allowed")
	case *kind.FalseSchema:
		return domain.T("schema_false", "this property is not allowed here")
	}
	return domain.Text{Message: k.LocalizedString(printer)}
}

// primitives shows each value as the schema writes it; ok is false when one is an object or an array.
func primitives(values []any) ([]string, bool) {
	out := make([]string, 0, len(values))
	for _, v := range values {
		switch v := v.(type) {
		case []any, map[string]any:
			return nil, false
		case string:
			out = append(out, strconv.Quote(v))
		default:
			out = append(out, fmt.Sprint(v))
		}
	}
	return out, true
}

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return strings.Join(quoted, ", ")
}

// plural picks the en-US template for a count; the desktop picks its own form from the count param.
func plural(n int, one, other string) string {
	if n == 1 {
		return one
	}
	return other
}

// Locate finds the line and column of a JSON-pointer location in a YAML node tree; it falls back to the deepest
// ancestor found.
func Locate(root *yaml.Node, location []string) (int, int) {
	n := root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	line, col := n.Line, n.Column
	for _, seg := range location {
		switch n.Kind {
		case yaml.MappingNode:
			found := false
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == seg {
					line, col = n.Content[i].Line, n.Content[i].Column
					n = n.Content[i+1]
					found = true
					break
				}
			}
			if !found {
				return line, col
			}
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx >= len(n.Content) {
				return line, col
			}
			n = n.Content[idx]
			line, col = n.Line, n.Column
		default:
			return line, col
		}
	}
	return line, col
}

// LocatePointer is Locate for a "/a/b/0" pointer.
func LocatePointer(root *yaml.Node, pointer string) (int, int) {
	if root == nil {
		return 0, 0
	}
	p := strings.Trim(pointer, "/")
	if p == "" {
		return Locate(root, nil)
	}
	return Locate(root, strings.Split(p, "/"))
}
