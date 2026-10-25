package domain

import (
	"path/filepath"
	"strings"
)

// PathRule is where a path that lets a harness write may lead: a task's workspace, its repositories' local paths, and
// the writable paths of a custom profile. It checks a path ResolvePath returned, so neither a .. segment nor a symlink
// leads out. The check is defense in depth, not a barrier against a caller that holds the API's
// token, who can run any program through a script task.
type PathRule struct {
	home    string
	dataDir string
	volumes string
	allowed []string
}

// NewPathRule builds the rule for a user's home directory, HarmFlow's data directory, the directory where other volumes
// are mounted (/Volumes on macOS, empty where there is none), and the allowed folders of Settings, each absolute.
func NewPathRule(home, dataDir, volumes string, allowed []string) PathRule {
	r := PathRule{home: resolveOrClean(home), dataDir: resolveOrClean(dataDir)}
	if volumes != "" {
		r.volumes = resolveOrClean(volumes)
	}
	for _, a := range allowed {
		if filepath.IsAbs(a) {
			r.allowed = append(r.allowed, resolveOrClean(a))
		}
	}
	return r
}

func resolveOrClean(p string) string {
	if real, err := ResolvePath(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// ResolvePath returns the path p leads to: its symlinks resolved and its .. segments applied, in the order the
// operating system applies them. A path not created yet resolves through the longest part of it that exists.
func ResolvePath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", TextErr("path_not_absolute", `"{{path}}" is not an absolute path`, "path", p)
	}
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			parts = append(parts, s)
		}
	}
	for n := len(parts); n > 0; n-- {
		real, err := filepath.EvalSymlinks("/" + strings.Join(parts[:n], "/"))
		if err == nil {
			return filepath.Clean(filepath.Join(append([]string{real}, parts[n:]...)...)), nil
		}
	}
	return filepath.Clean("/" + strings.Join(parts, "/")), nil
}

// Check returns nil when the resolved path p lies inside the user's home directory, inside a mounted volume, or inside
// an allowed folder, and in no place Forbidden names.
func (r PathRule) Check(p string) error {
	if err := r.Forbidden(p); err != nil {
		return err
	}
	if below(r.home, p) {
		return nil
	}
	if r.volumes != "" && below(r.volumes, p) && strings.Contains(rel(r.volumes, p), string(filepath.Separator)) {
		return nil
	}
	for _, a := range r.allowed {
		if within(a, p) {
			return nil
		}
	}
	return TextErr("path_outside_allowed_area",
		"{{path}} is outside your home folder, the mounted volumes, and the allowed folders in Settings", "path", p)
}

// Forbidden returns an error when the resolved path p is the home directory or one of its ancestors, or lies inside or
// contains HarmFlow's data directory, ~/Library, or a hidden folder at the top of the home directory: where the
// credentials, the harnesses' configuration, the login items, and HarmFlow's task files live.
func (r PathRule) Forbidden(p string) error {
	if within(p, r.home) {
		return TextErr("path_forbidden_home", "{{path}} is your home folder or contains it; choose a folder inside it",
			"path", p)
	}
	place := ""
	switch {
	case within(r.dataDir, p) || within(p, r.dataDir):
		place = r.dataDir
	case within(filepath.Join(r.home, "Library"), p):
		place = "~/Library"
	case below(r.home, p) && strings.HasPrefix(rel(r.home, p), "."):
		place = "~/" + strings.SplitN(rel(r.home, p), string(filepath.Separator), 2)[0]
	}
	if place != "" {
		return TextErr("path_forbidden_place", "{{path}} is inside or contains {{place}}, where no harness may write",
			"path", p, "place", place)
	}
	return nil
}

func rel(dir, p string) string {
	r, err := filepath.Rel(dir, p)
	if err != nil {
		return ".."
	}
	return r
}

// within reports whether p is dir or lies below it.
func within(dir, p string) bool {
	r := rel(dir, p)
	return r == "." || (r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
}

// below reports whether p lies below dir, not at dir itself.
func below(dir, p string) bool { return within(dir, p) && rel(dir, p) != "." }
