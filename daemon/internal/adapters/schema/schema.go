// Package schema validates task documents, task templates, execution reports, and events against the embedded copies
// of contracts/schemas, and locates each problem in the YAML by line and column.
package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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

// Validator holds the task, template, execution report, event, and session context schemas, compiled, and the task
// schema's identifier and skill name.
type Validator struct {
	task       *jsonschema.Schema
	template   *jsonschema.Schema
	identifier *jsonschema.Schema
	skillName  *jsonschema.Schema
	report     *jsonschema.Schema
	event      *jsonschema.Schema
	context    *jsonschema.Schema
}

// New compiles the embedded schemas; an error means an embedded schema does not compile.
func New() (*Validator, error) {
	c := jsonschema.NewCompiler()
	// An event's timestamp is checked as a date-time; the task and report schemas name no format.
	c.AssertFormat()
	for url, raw := range map[string][]byte{
		contracts.TaskSchemaURL:     contracts.TaskSchema,
		contracts.TemplateSchemaURL: contracts.TemplateSchema,
		contracts.ReportSchemaURL:   contracts.ExecutionReportSchema,
		contracts.EventSchemaURL:    contracts.EventSchema,
		contracts.ContextSchemaURL:  contracts.ContextSchema,
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
	template, err := c.Compile(contracts.TemplateSchemaURL)
	if err != nil {
		return nil, err
	}
	identifier, err := c.Compile(contracts.TaskSchemaURL + "#/$defs/identifier")
	if err != nil {
		return nil, err
	}
	skillName, err := c.Compile(contracts.TaskSchemaURL + "#/$defs/skillName")
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
	sessionContext, err := c.Compile(contracts.ContextSchemaURL)
	if err != nil {
		return nil, err
	}
	return &Validator{task: task, template: template, identifier: identifier, skillName: skillName, report: report,
		event: event, context: sessionContext}, nil
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
		// yamlLine matched digits, so these conversions succeed.
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

// ValidateTemplate checks a decoded document against template.schema.json; node, when given, locates each issue.
func (v *Validator) ValidateTemplate(doc any, node *yaml.Node) []Issue {
	return collect(v.template.Validate(doc), node)
}

// ValidIdentifier says whether name is an identifier of task.schema.json, the name a task cites an agent by.
func (v *Validator) ValidIdentifier(name string) bool {
	return v.identifier.Validate(name) == nil
}

// ValidSkillName says whether name is a skill name of task.schema.json, the name a task cites a skill by: an
// identifier, or a plugin's skill as `<plugin>:<skill>`.
func (v *Validator) ValidSkillName(name string) bool {
	return v.skillName.Validate(name) == nil
}

// ValidateReport checks report.json against execution-report.schema.json.
func (v *Validator) ValidateReport(doc any) []Issue {
	return collect(v.report.Validate(doc), nil)
}

// ValidateEvent checks an event, decoded from the JSON its log line or SSE frame carries, against event.schema.json.
func (v *Validator) ValidateEvent(doc any) []Issue {
	return collect(v.event.Validate(doc), nil)
}

// ValidateContext checks a context.json the daemon wrote against context.schema.json.
func (v *Validator) ValidateContext(doc any) []Issue {
	return collect(v.context.Validate(doc), nil)
}

// declaredSchemaURL is where a schema a skill or an agent declares is compiled; it refers to nothing else.
const declaredSchemaURL = "https://harmflow.com/schemas/v1alpha1/declared-parameter.schema.json"

// CheckDeclared checks a value against a JSON Schema a skill or an agent declares for an object parameter, both as YAML
// or JSON decoded them: the problems found, each as "<pointer>: <text>" in en-US, none for a value that matches. An
// error says the schema cannot be compiled.
func (v *Validator) CheckDeclared(schemaDoc, value any) ([]string, error) {
	doc, err := asJSON(schemaDoc)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(declaredSchemaURL, doc); err != nil {
		return nil, err
	}
	s, err := c.Compile(declaredSchemaURL)
	if err != nil {
		return nil, err
	}
	inst, err := asJSON(value)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, is := range collect(s.Validate(inst), nil) {
		path := is.Path
		if path == "" {
			path = "/"
		}
		out = append(out, path+": "+is.Message)
	}
	return out, nil
}

// asJSON gives a value as the validator reads JSON, the numbers YAML decodes as integers included.
func asJSON(v any) (any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
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
	add := func(location []string, text domain.Text) {
		path := "/" + strings.Join(location, "/")
		if len(location) == 0 {
			path = ""
		}
		key := path + "|" + text.Message
		if seen[key] {
			return
		}
		seen[key] = true
		is := Issue{Path: path, Text: text, Code: "schema"}
		if node != nil {
			is.Line, is.Column = Locate(node, location)
		}
		out = append(out, is)
	}
	for _, l := range leaves {
		// A missing property is reported at its own path, where a form shows it, and located at the object that lacks
		// it, the deepest node the text has.
		if r, ok := l.ErrorKind.(*kind.Required); ok {
			for _, name := range r.Missing {
				add(append(slices.Clip(l.InstanceLocation), name), kindText(&kind.Required{Missing: []string{name}}))
			}
			continue
		}
		add(l.InstanceLocation, kindText(l.ErrorKind))
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
