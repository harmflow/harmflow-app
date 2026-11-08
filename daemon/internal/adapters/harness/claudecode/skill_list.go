package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/procutil"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// defaultSkillListTimeout bounds the session started to read Claude Code's skill list, up to its init line: its
// start-up, its sign-in, and the settings and plugins it loads.
const defaultSkillListTimeout = 60 * time.Second

// skillListGrace is how long the listing session has to end once it is stopped, before it is killed.
const skillListGrace = 2 * time.Second

// skillListPrompt is the prompt of the session started to read the skill list. The session is stopped at its init
// line, before the model answers it.
const skillListPrompt = "Reply with OK."

// skillListCommand names the listing session in a text for the user.
const skillListCommand = "claude -p"

// streamHead is the part of a stream-json line that says what the line is.
type streamHead struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
}

// initSkills is the skill list of the init line, as Claude Code 2.1.309 writes it: the names a session may load, its
// own bundled skills, the user's, the project's, those of the folders it adds, and a plugin's as <plugin>:<skill>.
type initSkills struct {
	Skills *[]string `json:"skills"`
}

// installedSkills are the skills the installed Claude Code loads for a session in the user's home folder, with no
// folder added, as it reports them, sorted by name and each once: its bundled skills, the user's, and those of the
// plugins installed. HarmFlow does not search Claude Code's folders: it starts `claude -p` there, as an execution
// starts it, reads the skills of the stream's init line, and stops the session before the model answers. The list is of
// the installation, the same whatever the task's workspace, so a skill of a task's own project, or of a folder it adds,
// is not on it. Each CLI's list is kept until ReloadUserItems, since reading it starts a session that takes seconds;
// one that failed is not kept. A second lookup shares the listing already running, and a lookup whose context ends
// returns while the listing goes on for the others; the last one to leave stops it. A CLI that was not detected cannot
// answer, which is an error, never an empty list. A listed skill of the user's own carries its folder, for what its
// SKILL.md declares (userSkillPath).
func (a *Adapter) installedSkills(ctx context.Context, info ports.HarnessInfo) ([]ports.ResolvedItem, error) {
	if !info.Detected || info.Path == "" {
		return nil, domain.TextErr("harness_not_detected", "{{harness}} was not detected on this machine", "harness", Name)
	}
	key := info.Path
	a.mu.Lock()
	if items, ok := a.skills[key]; ok {
		a.mu.Unlock()
		return items, nil
	}
	l, ok := a.listings[key]
	if !ok {
		// The listing outlives a lookup whose context ends, for the lookups that share it, until the last one leaves.
		listCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		l = &skillListing{done: make(chan struct{}), cancel: cancel}
		if a.listings == nil {
			a.listings = map[string]*skillListing{}
		}
		a.listings[key] = l
		go a.runListing(listCtx, info, key, l)
	}
	l.waiters++
	a.mu.Unlock()
	select {
	case <-l.done:
		return l.items, l.err
	case <-ctx.Done():
		a.mu.Lock()
		defer a.mu.Unlock()
		if l.waiters--; l.waiters == 0 {
			// Nobody waits for the listing any more: stop it, and let the next lookup start its own.
			if a.listings[key] == l {
				delete(a.listings, key)
			}
			l.cancel()
		}
		return nil, ctx.Err()
	}
}

// skillListing is one listing session in flight, which the lookups of its CLI share. Its items and err are set before
// done is closed, and read only after; waiters, guarded by the adapter's mutex, counts the lookups waiting for it.
type skillListing struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	items   []ports.ResolvedItem
	err     error
}

// runListing lists the skills of key into l, keeps them when the listing succeeded, and closes l.done. A listing that
// ReloadUserItems dropped, or that nobody waited for any more, does not write its result into the cache.
func (a *Adapter) runListing(ctx context.Context, info ports.HarnessInfo, key string, l *skillListing) {
	defer l.cancel()
	names, err := a.listSkills(ctx, info)
	if err == nil {
		slices.Sort(names)
		for _, n := range slices.Compact(names) {
			if n != "" {
				l.items = append(l.items, ports.ResolvedItem{Name: n, Kind: "skill", Path: a.userSkillPath(n)})
			}
		}
	}
	l.err = err
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.listings[key] == l {
		delete(a.listings, key)
		if err == nil {
			if a.skills == nil {
				a.skills = map[string][]ports.ResolvedItem{}
			}
			a.skills[key] = l.items
		}
	}
	close(l.done)
}

// userSkillPath is the folder of a skill the list names, when it is the user's own, in the configuration directory's
// skills folder, so that what its SKILL.md declares can be read; "" for a skill bundled with Claude Code or a plugin's,
// whose file HarmFlow does not look for.
func (a *Adapter) userSkillPath(name string) string {
	if strings.ContainsAny(name, `:/\`) || name == "." || name == ".." {
		return ""
	}
	dir := filepath.Join(a.configDir(), "skills", name)
	if st, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil || st.IsDir() {
		return ""
	}
	return dir
}

// listSkills starts a Claude Code session in the user's home folder, with the flags an execution gives it that change
// what it loads, and reads the skill list of its init line. Its environment is the one ports.SessionEnv gives an
// execution's session, apart from the execution's own HARMFLOW_* identifiers, which a listing has none of. The built-in
// skills, which an execution adds as a plugin of its own, are not asked for: HarmFlow finds them by their prefix. A
// session that ends, prints no init line HarmFlow can read, or reaches the timeout first fails the listing.
func (a *Adapter) listSkills(ctx context.Context, info ports.HarnessInfo) ([]string, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if info.HasFeature("--no-session-persistence") {
		args = append(args, "--no-session-persistence")
	}
	init := make(chan []byte, 1) // the first init line, which ends the wait
	sent := false
	inv := ports.Invocation{Path: info.Path, Args: args, Env: a.withToken(ports.SessionEnv(a.Env)), Dir: a.Home,
		Stdin: skillListPrompt}
	p, err := procutil.StartLines(inv, io.Discard, func(line []byte) {
		var h streamHead
		if !sent && json.Unmarshal(line, &h) == nil && h.Type == "system" && h.Subtype == "init" {
			sent = true
			init <- slices.Clone(line)
		}
	}, nil)
	if err != nil {
		return nil, skillListFailed(err.Error())
	}
	timeout := a.SkillListTimeout
	if timeout == 0 {
		timeout = defaultSkillListTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	// Every way out stops the session, and a session that ended on its own still has what it left running ended.
	defer func() {
		p.Stop(skillListGrace)
		<-p.Done()
	}()
	select {
	case line := <-init:
		return initSkillNames(line)
	case <-p.Done():
		// Every line is read once the session is done, so an init line it printed before it ended is in the channel.
		select {
		case line := <-init:
			return initSkillNames(line)
		default:
		}
		if stderr := strings.TrimSpace(p.StderrTail()); stderr != "" {
			return nil, skillListFailed(stderr)
		}
		return nil, domain.TextErr("harness_exit_code", "{{harness}} exited with code {{code}}", "harness", Name,
			"code", strconv.Itoa(p.ExitCode()))
	case <-timer.C:
		return nil, domain.TextErr("claude_skill_list_timed_out",
			"{{command}} printed no init line within {{timeout}}, so the skills Claude Code loads are unknown",
			"command", skillListCommand, "timeout", timeout.String())
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// initSkillNames reads the skill names of an init line; a line with no list of names is output HarmFlow cannot read.
func initSkillNames(line []byte) ([]string, error) {
	var l initSkills
	if json.Unmarshal(line, &l) != nil || l.Skills == nil {
		return nil, domain.TextErr("claude_skill_list_unrecognized", "{{command}} printed no skill list HarmFlow can read",
			"command", skillListCommand)
	}
	return *l.Skills, nil
}

func skillListFailed(detail string) error {
	return domain.TextErr("command_failed", "{{command}} failed: {{detail}}", "command", skillListCommand,
		"detail", detail)
}

// ListsInstallation implements ports.InstallationLister: skills come from the list the installed Claude Code reports,
// agents from its configuration directory.
func (a *Adapter) ListsInstallation(kind string) bool { return kind == "skill" }

// AllowsUnverified implements ports.UnverifiedItemsAllower: Claude Code loads a task's skills itself, so a skill list
// it could not give leaves them unverified rather than missing.
func (a *Adapter) AllowsUnverified(kind string) bool { return kind == "skill" }

// ReloadUserItems implements ports.ItemReloader: the next lookup of the skills asks Claude Code for its list again. It
// does not wait for a listing in flight: the lookups already waiting for one get its result, which is not kept.
func (a *Adapter) ReloadUserItems() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.skills = nil
	a.listings = nil
}
