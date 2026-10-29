package integration

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every file of this checkout's templates/ is a valid built-in template, listed in the order of its identifier, and a
// task created from each, in each language of the interface, saves once the values the template leaves unset are
// filled. Nothing is written until then.
func TestBuiltinTemplatesSave(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	repo := t.TempDir()
	d.call("PATCH", "/settings", map[string]any{"allowedFolders": []any{repo}}, 200)
	entries, err := os.ReadDir(filepath.Join(repoRoot(), "templates"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range entries {
		files = append(files, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	var listed []string
	for _, tp := range d.list("/templates") {
		id, _ := tp["id"].(string)
		listed = append(listed, id)
		if v, _ := tp["validation"].(map[string]any); v["valid"] != true || tp["source"] != "builtin" {
			t.Errorf("GET /templates lists %s as %v, want a valid built-in template", id, tp)
		}
	}
	if len(files) == 0 || !slices.Equal(listed, files) {
		t.Fatalf("GET /templates = %v, want the files of templates/, %v", listed, files)
	}
	for _, id := range listed {
		for _, language := range []string{"pt-BR", "en-US", "es-ES"} {
			t.Run(id+" in "+language, func(t *testing.T) {
				d := d.in(t)
				doc := d.call("POST", "/templates/"+id+"/draft", map[string]any{"language": language}, 200)["document"].(map[string]any)
				if tasks := d.list("/tasks"); len(tasks) != 0 {
					t.Fatalf("GET /tasks after a draft = %v, want no task", tasks)
				}
				fill(doc, repo)
				saved := d.call("POST", "/tasks", map[string]any{"document": doc}, 201)
				if errs, _ := saved["validation"].(map[string]any)["errors"].([]any); len(errs) != 0 {
					t.Errorf("POST /tasks from %s: errors %v", id, errs)
				}
				d.call("DELETE", "/tasks/"+saved["id"].(string), nil, 204)
			})
		}
	}
}

// fill sets in a new task's document the values a built-in template leaves for the editor to ask for: a prompt, a
// repository's name and local path, and a program.
func fill(doc map[string]any, repo string) {
	spec := doc["spec"].(map[string]any)
	if spec["type"] == "script" {
		spec["script"].(map[string]any)["command"] = []any{"/bin/echo", "done"}
		return
	}
	if _, ok := spec["prompt"]; !ok {
		spec["prompt"] = "Do the work."
	}
	if gh, ok := spec["github"].(map[string]any); ok {
		for _, r := range gh["repositories"].([]any) {
			r.(map[string]any)["name"], r.(map[string]any)["localPath"] = "harmflow/example", repo
		}
	}
}

// Built-in templates are read-only over the API, and an unknown one, or a language the interface lacks, builds no task.
func TestBuiltinTemplatesAreReadOnlyOverTheAPI(t *testing.T) {
	t.Parallel()
	d := startDaemon(t, t.TempDir())
	before := d.list("/templates")
	for _, c := range []struct {
		method, path string
		body         any
		want         int
		key          string
	}{
		{"PUT", "/templates/hf-general", map[string]any{"yaml": "kind: TaskTemplate\n"}, 409, "template_read_only"},
		{"DELETE", "/templates/hf-general", nil, 409, "template_read_only"},
		{"PUT", "/templates/hf-new", map[string]any{"yaml": "kind: TaskTemplate\n"}, 409, "template_read_only"},
		{"DELETE", "/templates/mine", nil, 404, "template_not_found"},
		{"POST", "/templates/hf-none/draft", map[string]any{"language": "en-US"}, 404, "template_not_found"},
		{"POST", "/templates/hf-general/draft", map[string]any{"language": "fr-FR"}, 400, "template_language_invalid"},
	} {
		got := d.call(c.method, c.path, c.body, c.want)
		if e, _ := got["error"].(map[string]any); e["messageKey"] != c.key {
			t.Errorf("%s %s = %v, want %s", c.method, c.path, got, c.key)
		}
	}
	if after := d.list("/templates"); len(after) != len(before) {
		t.Errorf("GET /templates after the refusals = %v, want %v", after, before)
	}
}
