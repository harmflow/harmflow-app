package claudecode

import (
	"context"
	"encoding/json"
	"io"
	"os"
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

// sessionSkills are the skills Claude Code loads for a session in workspace with the added folders, as it reports them,
// sorted by name and each once. HarmFlow does not search Claude Code's folders: it starts `claude -p` there, as an
// execution starts it, reads the skills of the stream's init line, and stops the session before the model answers. A
// workspace not made yet has no skills of its own, so its list is asked for in the user's home folder, and an added
// folder not made yet, such as a repository not cloned, is left out, as it holds none. The list of each
// CLI, folder, and set of added folders is kept until ReloadUserItems, since reading it starts a session that takes
// seconds; one that failed is not kept. A CLI that was not detected cannot answer, which is an error, never an empty
// list.
func (a *Adapter) sessionSkills(ctx context.Context, info ports.HarnessInfo, workspace string, added []string) ([]ports.ResolvedItem, error) {
	if !info.Detected || info.Path == "" {
		return nil, domain.TextErr("harness_not_detected", "{{harness}} was not detected on this machine", "harness", Name)
	}
	dir := workspace
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		dir = a.Home
	}
	added = slices.DeleteFunc(uniq(slices.Sorted(slices.Values(added))), func(d string) bool {
		st, err := os.Stat(d)
		return err != nil || !st.IsDir()
	})
	key := strings.Join(append([]string{info.Path, dir}, added...), "\x00")
	a.mu.Lock()
	defer a.mu.Unlock()
	if items, ok := a.skills[key]; ok {
		return items, nil
	}
	names, err := a.listSkills(ctx, info, dir, added)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	var items []ports.ResolvedItem
	for _, n := range slices.Compact(names) {
		if n != "" {
			items = append(items, ports.ResolvedItem{Name: n, Kind: "skill"})
		}
	}
	if a.skills == nil {
		a.skills = map[string][]ports.ResolvedItem{}
	}
	a.skills[key] = items
	return items, nil
}

// listSkills starts a Claude Code session in dir with the added folders, with the flags an execution gives it that
// change what it loads, and reads the skill list of its init line. The built-in skills, which an execution adds as a
// plugin of its own, are not asked for: HarmFlow finds them by their prefix. A session that ends, prints no init line
// HarmFlow can read, or reaches the timeout first fails the listing.
func (a *Adapter) listSkills(ctx context.Context, info ports.HarnessInfo, dir string, added []string) ([]string, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if info.HasFeature("--no-session-persistence") {
		args = append(args, "--no-session-persistence")
	}
	for _, d := range added {
		if d != dir {
			args = append(args, "--add-dir", d)
		}
	}
	init := make(chan []byte, 1) // the first init line, which ends the wait
	sent := false
	p, err := procutil.StartLines(ports.Invocation{Path: info.Path, Args: args, Env: a.Env, Dir: dir, Stdin: skillListPrompt},
		io.Discard, func(line []byte) {
			var h streamHead
			if !sent && json.Unmarshal(line, &h) == nil && h.Type == "system" && h.Subtype == "init" {
				sent = true
				init <- slices.Clone(line)
			}
		}, nil)
	if err != nil {
		return nil, skillListFailed(err.Error())
	}
	timeout := a.skillListTimeout
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

// ListsPerWorkspace implements ports.WorkspaceLister: skills come from the list Claude Code reports for the workspace,
// agents from its configuration directory.
func (a *Adapter) ListsPerWorkspace(kind string) bool { return kind == "skill" }

// AllowsUnverified implements ports.UnverifiedItemsAllower: Claude Code loads a task's skills itself, so a skill list
// it could not give leaves them unverified rather than missing.
func (a *Adapter) AllowsUnverified(kind string) bool { return kind == "skill" }

// ReloadUserItems implements ports.ItemReloader: the next lookup of the skills asks Claude Code for its list again.
func (a *Adapter) ReloadUserItems() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.skills = nil
}
