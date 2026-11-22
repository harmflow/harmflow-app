package app

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// customEntriesState is the state file that keeps the custom entries across restarts.
const customEntriesState = "custom-entries"

// customEntriesFile is what customEntriesState holds: the entries, in the order added, and the number the next one's
// identifier takes, which only grows, so an identifier is never given twice.
type customEntriesFile struct {
	Entries []customEntry `json:"entries"`
	Next    int           `json:"next"`
}

// customEntryPrefix starts the installation identifier of every custom entry, which no home detection finds starts
// with.
const customEntryPrefix = "custom-"

// customEntry is an installation the owner added from a command line: its identifier, which a task's installation key
// names it by, its harness, its name, and the line as typed. What the line assigns is read from Line each time, never
// kept apart, so nothing else holds an assignment's value.
type customEntry struct {
	ID      string `json:"id"`
	Harness string `json:"harness"`
	Name    string `json:"name"`
	Line    string `json:"line"`
}

// entryLine is a command line read: its `NAME=value` assignments, expanded, its executable as written, and its
// arguments.
type entryLine struct {
	assignments []string
	executable  string
	args        []string
}

var assignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// The shell syntax a command line may hold, which the daemon refuses rather than run through a shell: each refusal
// names one by its code, which the interface translates, beside its en-US name.
const (
	syntaxAnd          = "and"
	syntaxOr           = "or"
	syntaxSubstitution = "substitution"
	syntaxPipe         = "pipe"
	syntaxSeparator    = "separator"
	syntaxBackground   = "background"
	syntaxRedirection  = "redirection"
	syntaxNewLine      = "new_line"
	syntaxVariable     = "variable"
)

// syntaxNames are the en-US names of the shell syntax codes.
var syntaxNames = map[string]string{
	syntaxAnd:          "&&",
	syntaxOr:           "||",
	syntaxSubstitution: "a command substitution",
	syntaxPipe:         "a pipe, |",
	syntaxSeparator:    "a command separator, ;",
	syntaxBackground:   "a background &",
	syntaxRedirection:  "a redirection, > or <",
	syntaxNewLine:      "a new line",
	syntaxVariable:     "a variable other than $HOME",
}

// shellSyntax are the tokens of the shell syntax the daemon looks for outside quotes, longest first, each with its
// code.
var shellSyntax = []struct{ token, code string }{
	{"&&", syntaxAnd},
	{"||", syntaxOr},
	{"$(", syntaxSubstitution},
	{"`", syntaxSubstitution},
	{"|", syntaxPipe},
	{";", syntaxSeparator},
	{"&", syntaxBackground},
	{">", syntaxRedirection},
	{"<", syntaxRedirection},
}

// parseEntryLine reads a custom entry's command line as zero or more `NAME=value` assignments, then an executable, then
// arguments, with no shell. Words split on blanks; single quotes keep what they hold, double quotes keep blanks, and a
// backslash keeps the character after it. `$HOME`, `${HOME}`, and a `~` that starts a word or a value expand to home,
// and nothing else does: shell syntax outside single quotes, or another variable, refuses the line, naming what it
// found.
func parseEntryLine(line, home string) (entryLine, error) {
	var words []string
	var word strings.Builder
	inWord := false
	quote := byte(0)
	wordStart := true // the next character starts a word, or a value after NAME=
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			continue
		case c == '\\' && quote == 0:
			if i+1 < len(line) {
				i++
				word.WriteByte(line[i])
				inWord, wordStart = true, false
			}
			continue
		case c == '\'' && quote == 0:
			quote, inWord, wordStart = '\'', true, false
			continue
		case c == '"':
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
			inWord, wordStart = true, false
			continue
		case (c == ' ' || c == '\t') && quote == 0:
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
			wordStart = true
			continue
		case c == '\n' || c == '\r':
			if quote == 0 {
				return entryLine{}, shellSyntaxFound(syntaxNewLine)
			}
		}
		if c == '$' {
			switch {
			case strings.HasPrefix(line[i:], "${HOME}"):
				word.WriteString(home)
				i += len("${HOME}") - 1
				inWord, wordStart = true, false
				continue
			case strings.HasPrefix(line[i:], "$HOME") && !isNameByte(line, i+len("$HOME")):
				word.WriteString(home)
				i += len("$HOME") - 1
				inWord, wordStart = true, false
				continue
			case strings.HasPrefix(line[i:], "$("):
				return entryLine{}, shellSyntaxFound(syntaxSubstitution)
			case i+1 < len(line) && (line[i+1] == '{' || isNameStart(line[i+1])):
				return entryLine{}, shellSyntaxFound(syntaxVariable)
			}
		}
		if quote == 0 {
			for _, s := range shellSyntax {
				if strings.HasPrefix(line[i:], s.token) {
					return entryLine{}, shellSyntaxFound(s.code)
				}
			}
		} else if c == '`' {
			return entryLine{}, shellSyntaxFound(syntaxSubstitution)
		}
		if c == '~' && wordStart && quote == 0 && (i+1 == len(line) || strings.IndexByte("/ \t", line[i+1]) >= 0) {
			word.WriteString(home)
			inWord, wordStart = true, false
			continue
		}
		word.WriteByte(c)
		inWord = true
		// A `~` right after NAME= starts the value, as one that starts a word does.
		wordStart = c == '=' && assignmentRe.MatchString(word.String()) && strings.Count(word.String(), "=") == 1
	}
	if quote != 0 {
		return entryLine{}, domain.TextErr("custom_entry_unclosed_quote", "the command line has a quote that is never closed")
	}
	if inWord {
		words = append(words, word.String())
	}
	var out entryLine
	i := 0
	for ; i < len(words) && assignmentRe.MatchString(words[i]); i++ {
		out.assignments = append(out.assignments, words[i])
	}
	if i == len(words) {
		return entryLine{}, domain.TextErr("custom_entry_no_executable",
			"the command line names no executable after its assignments")
	}
	out.executable, out.args = words[i], words[i+1:]
	return out, nil
}

func isNameStart(c byte) bool { return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

// isNameByte reports whether line has, at i, a character that would continue a variable's name.
func isNameByte(line string, i int) bool {
	if i >= len(line) {
		return false
	}
	c := line[i]
	return isNameStart(c) || c >= '0' && c <= '9'
}

// shellSyntaxFound refuses a line for the shell syntax code names, giving both the code and its en-US name.
func shellSyntaxFound(code string) error {
	return domain.TextErr("custom_entry_shell_syntax",
		"the command line holds {{syntaxName}}, which is shell syntax: the daemon starts the command itself, with no shell",
		"syntax", code, "syntaxName", syntaxNames[code])
}

// loadCustomEntries reads the custom entries kept across restarts; a file that cannot be read leaves none.
func (a *App) loadCustomEntries() {
	var file customEntriesFile
	if _, err := a.d.State.Load(customEntriesState, &file); err != nil {
		a.log.Printf("loading the custom harness entries; none is listed until they are added again: %v", err)
		return
	}
	a.mu.Lock()
	a.customEntries, a.nextEntry = file.Entries, file.Next
	a.mu.Unlock()
}

// saveCustomEntriesLocked keeps the custom entries, and the number the next one takes, across restarts. a.mu is held.
func (a *App) saveCustomEntriesLocked() error {
	return a.d.State.Save(customEntriesState, customEntriesFile{Entries: slices.Clone(a.customEntries), Next: a.nextEntry})
}

// probeCustomEntries probes each custom entry of the harness ad with its own assignments, in the order they were added.
// An entry whose line no longer reads, or whose executable is gone, is listed not installed, so a task naming it still
// finds it, and reads it not ready.
func (a *App) probeCustomEntries(ctx context.Context, ad ports.HarnessAdapter) []ports.HarnessInfo {
	a.mu.Lock()
	var entries []customEntry
	for _, e := range a.customEntries {
		if e.Harness == ad.ID() {
			entries = append(entries, e)
		}
	}
	a.mu.Unlock()
	var out []ports.HarnessInfo
	for _, e := range entries {
		out = append(out, a.probeCustomEntry(ctx, ad, e))
	}
	return out
}

func (a *App) probeCustomEntry(ctx context.Context, ad ports.HarnessAdapter, e customEntry) ports.HarnessInfo {
	info := ports.HarnessInfo{ID: ad.ID(), Name: ad.Name(), Authentication: "unknown", Readiness: "not_installed",
		Models: []ports.ModelInfo{}, Efforts: []string{}, EnforceableControls: []string{}, Installations: []string{}}
	parsed, err := parseEntryLine(e.Line, a.d.Paths.UserHome)
	prober, ok := ad.(ports.EntryProber)
	path := ""
	if err == nil && ok {
		path = prober.EntryExecutable(parsed.executable)
	}
	switch {
	case err != nil:
		info.SetDiagnostic(domain.TextOf(err))
	case !ok:
		info.SetDiagnostic(domain.T("custom_entry_unsupported", "{{harness}} takes no custom entries", "harness", ad.Name()))
	case path == "":
		info.SetDiagnostic(entryExecutableMissing(parsed.executable))
	default:
		info = prober.ProbeEntry(ctx, path, parsed.assignments)
		info.EntryArgs = parsed.args
	}
	info.InstallationID, info.EntryName, info.EntryLine, info.PathSource = e.ID, e.Name, e.Line, "custom"
	info.Homes, info.CustomEntries = nil, nil
	return info
}

func entryExecutableMissing(name string) domain.Text {
	return domain.T("custom_entry_executable_missing", "no executable file {{executable}}, as a path or on the daemon's PATH",
		"executable", name)
}

// CustomEntryInput is what adding or editing a custom entry gives: its command line, and its name; a nil field keeps
// what an edit had, and a new entry without a name takes its harness's name and a number.
type CustomEntryInput struct {
	Line *string `json:"line"`
	Name *string `json:"name"`
}

// AddCustomEntry adds an installation from a command line: it reads the line with no shell, checks its executable as a
// path set by hand is checked, finds the harness whose --version the executable names, keeps the entry, and returns
// that harness detected again, the entry among its CustomEntries. A line that does not read, or names neither harness,
// saves nothing.
func (a *App) AddCustomEntry(ctx context.Context, in CustomEntryInput) (ports.HarnessInfo, error) {
	if in.Line == nil {
		return ports.HarnessInfo{}, entryInvalid("/line", domain.T("custom_entry_line_missing", "the command line is required"))
	}
	harness, err := a.identifyEntry(ctx, *in.Line)
	if err != nil {
		return ports.HarnessInfo{}, err
	}
	a.mu.Lock()
	e := customEntry{ID: a.nextEntryIDLocked(), Harness: harness, Line: strings.TrimSpace(*in.Line)}
	if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
		e.Name = strings.TrimSpace(*in.Name)
	} else {
		e.Name = a.defaultEntryNameLocked(harness)
	}
	a.customEntries = append(a.customEntries, e)
	err = a.saveCustomEntriesLocked()
	a.mu.Unlock()
	if err != nil {
		a.log.Printf("saving the custom harness entries; they hold until the daemon stops: %v", err)
	}
	return a.detect(ctx, harness), nil
}

// UpdateCustomEntry renames a custom entry, gives it another command line, or both; a line is read and checked as
// AddCustomEntry reads it, and one that names the other harness moves the entry there. Both harnesses are detected
// again; the one the entry is in now is returned.
func (a *App) UpdateCustomEntry(ctx context.Context, id string, in CustomEntryInput) (ports.HarnessInfo, error) {
	a.mu.Lock()
	i := slices.IndexFunc(a.customEntries, func(e customEntry) bool { return e.ID == id })
	a.mu.Unlock()
	if i < 0 {
		return ports.HarnessInfo{}, entryNotFound(id)
	}
	harness := ""
	if in.Line != nil {
		h, err := a.identifyEntry(ctx, *in.Line)
		if err != nil {
			return ports.HarnessInfo{}, err
		}
		harness = h
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return ports.HarnessInfo{}, entryInvalid("/name", domain.T("custom_entry_name_empty", "the name cannot be empty"))
	}
	a.mu.Lock()
	i = slices.IndexFunc(a.customEntries, func(e customEntry) bool { return e.ID == id })
	if i < 0 {
		a.mu.Unlock()
		return ports.HarnessInfo{}, entryNotFound(id)
	}
	e := &a.customEntries[i]
	was := e.Harness
	if in.Line != nil {
		e.Line, e.Harness = strings.TrimSpace(*in.Line), harness
	}
	if in.Name != nil {
		e.Name = strings.TrimSpace(*in.Name)
	}
	now := e.Harness
	err := a.saveCustomEntriesLocked()
	a.mu.Unlock()
	if err != nil {
		a.log.Printf("saving the custom harness entries; they hold until the daemon stops: %v", err)
	}
	if was == now {
		return a.detect(ctx, now), nil
	}
	// The entry left its harness: a task naming it there no longer finds it, as after a removal.
	a.detect(ctx, was)
	info := a.detect(ctx, now)
	a.checkTasks(ctx)
	return info, nil
}

// RemoveCustomEntry removes a custom entry and returns its harness detected again; the installations detection finds
// stay as they are. A task that still names the entry fails validation from then on.
func (a *App) RemoveCustomEntry(ctx context.Context, id string) (ports.HarnessInfo, error) {
	a.mu.Lock()
	i := slices.IndexFunc(a.customEntries, func(e customEntry) bool { return e.ID == id })
	if i < 0 {
		a.mu.Unlock()
		return ports.HarnessInfo{}, entryNotFound(id)
	}
	harness := a.customEntries[i].Harness
	a.customEntries = slices.Delete(a.customEntries, i, i+1)
	err := a.saveCustomEntriesLocked()
	a.mu.Unlock()
	if err != nil {
		a.log.Printf("saving the custom harness entries; they hold until the daemon stops: %v", err)
	}
	info := a.detect(ctx, harness)
	a.checkTasks(ctx)
	return info, nil
}

// identifyEntry reads a command line and finds its harness: the one whose check of a path set by hand the executable
// passes, its --version naming it, as detection reads it. Each harness that takes custom entries finds the executable
// the line names its own way, as it finds its CLI.
func (a *App) identifyEntry(ctx context.Context, line string) (string, error) {
	parsed, err := parseEntryLine(line, a.d.Paths.UserHome)
	if err != nil {
		return "", entryInvalid("/line", domain.TextOf(err))
	}
	found := ""
	var names []string
	for _, id := range a.adapterIDs() {
		ad := a.adapters[id]
		identifier, identifies := ad.(ports.HarnessIdentifier)
		prober, probes := ad.(ports.EntryProber)
		if !identifies || !probes {
			continue
		}
		names = append(names, ad.Name())
		path := prober.EntryExecutable(parsed.executable)
		if path == "" {
			continue
		}
		if found == "" {
			found = path
		}
		if identifier.Identify(ctx, path) == nil {
			return id, nil
		}
	}
	if found == "" {
		return "", entryInvalid("/line", entryExecutableMissing(parsed.executable))
	}
	return "", entryInvalid("/line", domain.T("custom_entry_no_harness",
		"{{executable}} is none of {{harnesses}}: its --version names no harness HarmFlow runs", "executable", found,
		"harnesses", strings.Join(names, ", ")))
}

// nextEntryIDLocked is an identifier no custom entry has ever had: the counter kept with the entries, never lower than
// one past the highest an entry has, which only grows. a.mu is held.
func (a *App) nextEntryIDLocked() string {
	n := max(a.nextEntry, 1)
	for _, e := range a.customEntries {
		if k, err := strconv.Atoi(strings.TrimPrefix(e.ID, customEntryPrefix)); err == nil && k >= n {
			n = k + 1
		}
	}
	a.nextEntry = n + 1
	return fmt.Sprintf("%s%d", customEntryPrefix, n)
}

// defaultEntryNameLocked is the name of a new entry of a harness: the harness's name and the first number from 2 that
// no entry of it is named with, so the installation in use stays the harness's first. It never holds what the entry
// assigns. a.mu is held.
func (a *App) defaultEntryNameLocked(harness string) string {
	name := harness
	if ad, ok := a.adapters[harness]; ok {
		name = ad.Name()
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s %d", name, n)
		if !slices.ContainsFunc(a.customEntries, func(e customEntry) bool { return e.Name == candidate }) {
			return candidate
		}
	}
}

func entryInvalid(path string, text domain.Text) error {
	return &Error{Code: CodeValidationFailed, Text: text, Validation: fieldIssue(path, text)}
}

func entryNotFound(id string) error {
	return newError(CodeNotFound, domain.T("custom_entry_not_found", "no custom entry {{entry}}", "entry", id))
}
