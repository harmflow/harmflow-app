// Command harmflowd is the HarmFlow daemon: the scheduler, the executions, and the local API. It runs without the
// dashboard and stops nothing when the dashboard closes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/eventlog"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/fsstore"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/claudecode"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/codex"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/httpapi"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/launchd"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/schema"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/secret"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/sysinfo"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/tools"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/wake"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/workspace"
	"github.com/harmflow/harmflow-app/daemon/internal/app"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// Version is set at build time; desktop and daemon always ship at the same version.
var Version = "0.1.0"

// Commit is set at build time by make, which reads it from git also in a git worktree, where go build stamps none.
var Commit = ""

// BuiltAt is the time make built the daemon, in RFC 3339; see buildTime.
var BuiltAt = ""

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(Version)
		return
	}
	homeFlag := flag.String("home", "", "HarmFlow's data directory (default: $HARMFLOW_HOME or ~/Library/Application Support/HarmFlow)")
	builtinsFlag := flag.String("builtins", "", "directory holding the built-in agents/, skills/, and templates/")
	portFlag := flag.Int("port", 0, "port to listen on; 0 picks a free one")
	flag.Parse()
	if err := run(*homeFlag, *builtinsFlag, *portFlag); err != nil {
		fmt.Fprintln(os.Stderr, "harmflowd:", err)
		os.Exit(1)
	}
}

func run(homeFlag, builtinsFlag string, port int) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home := firstNonEmpty(homeFlag, os.Getenv("HARMFLOW_HOME"), filepath.Join(userHome, "Library", "Application Support", "HarmFlow"))
	paths := app.NewPaths(home, userHome, findBuiltins(builtinsFlag))
	for _, d := range []string{paths.Home, paths.Run, paths.Logs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	logFile, err := os.OpenFile(filepath.Join(paths.Logs, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close() // every line was written with O_APPEND; a failed close at exit loses nothing
	var logOut io.Writer = logFile
	if isTerminal(os.Stderr) {
		logOut = io.MultiWriter(logFile, os.Stderr)
	}
	logger := log.New(logOut, "", log.LstdFlags)

	lock, err := acquireLock(filepath.Join(paths.Run, "daemon.lock"), syscall.Flock)
	if err != nil {
		return err
	}
	defer lock.Close() // closing releases the flock; the process is exiting either way

	pathEnv := userPath(userHome)
	harnessEnv := withPath(os.Environ(), pathEnv)

	secrets, leftover := tokenStore(paths)
	token, err := startupToken(secrets, leftover, logger, time.Sleep)
	if err != nil {
		return err
	}

	tasks, err := fsstore.NewTasks(paths.Tasks)
	if err != nil {
		return err
	}
	state, err := fsstore.NewState(paths.State)
	if err != nil {
		return err
	}
	execs, err := fsstore.NewExecutions(paths.Executions)
	if err != nil {
		return err
	}
	events, err := eventlog.New(paths.Logs, time.Now)
	if err != nil {
		return err
	}
	validator, err := schema.New()
	if err != nil {
		return err
	}
	// Without its own path the daemon still runs; enabling autostart then fails rather than write an empty program.
	exe, err := os.Executable()
	if err != nil {
		logger.Printf("autostart is unavailable: finding the daemon's executable: %v", err)
	}
	daemonArgs := []string{}
	daemonEnv := map[string]string{"PATH": pathEnv}
	if homeFlag != "" || os.Getenv("HARMFLOW_HOME") != "" {
		daemonArgs = append(daemonArgs, "--home", home)
	}
	if paths.Builtins != "" {
		daemonArgs = append(daemonArgs, "--builtins", paths.Builtins)
	}
	for _, name := range []string{"HARMFLOW_TOKEN_STORE", "HARMFLOW_KEYCHAIN"} {
		if v := os.Getenv(name); v != "" {
			daemonEnv[name] = v
		}
	}
	system := sysinfo.New(paths.Home)
	a, err := app.New(app.Deps{
		Paths: paths, Version: Version, Commit: buildCommit(), BuiltAt: buildTime(BuiltAt), Wake: wake.NewMonitor(),
		Tasks: tasks, State: state, Executions: execs, Events: events, Validator: validator,
		Harnesses: []ports.HarnessAdapter{
			&claudecode.Adapter{Home: userHome, PathEnv: pathEnv, Env: harnessEnv},
			&codex.Adapter{Home: userHome, PathEnv: pathEnv, Env: harnessEnv, ClientVersion: Version},
		},
		Service:    launchd.NewAgent(userHome, filepath.Join(paths.Logs, "daemon.log")),
		System:     system,
		Tools:      &tools.Prober{Home: userHome, PathEnv: pathEnv, Env: harnessEnv},
		Workspace:  &workspace.Reader{PathEnv: pathEnv, Env: harnessEnv},
		HarnessEnv: harnessEnv, DaemonExecutable: exe, DaemonArgs: daemonArgs, DaemonEnv: daemonEnv, Logger: logger,
	})
	if err != nil {
		return err
	}
	srv := &httpapi.Server{App: a, Secrets: secrets, NewToken: secret.NewToken, Logger: logger}
	srv.SetToken(token)
	if err := srv.Listen(port); err != nil {
		return err
	}
	// The daemon's own context: what Start runs under ends when run returns, after Stop.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	system.Start(ctx)
	defer system.Stop()
	if err := a.Start(ctx); err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	if err := writeRuntime(paths.RuntimeFile, srv, secrets.Kind()); err != nil {
		return err
	}
	defer os.Remove(paths.RuntimeFile) // a file left behind is replaced at the next start
	logger.Printf("HarmFlow daemon %s listening on %s (home %s, builtins %s)", Version, srv.Address(), home, paths.Builtins)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		logger.Printf("stopping on %s", s)
		a.Stop(domain.T("daemon_signal", "the daemon received {{signal}}", "signal", s.String()))
	case <-a.ShutdownRequested:
		logger.Printf("stopping at the user's request")
		time.Sleep(200 * time.Millisecond) // let the API answer the stop request
		a.Stop(domain.T("daemon_stop_requested", "stopped at the user's request"))
	case err := <-serveErr:
		a.Stop(domain.T("local_api_stopped", "the local API stopped"))
		return err
	}
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("stopping the local API: %v", err)
	}
	logger.Printf("stopped")
	return nil
}

// writeRuntime publishes the address in a file only the user can read.
func writeRuntime(path string, srv *httpapi.Server, store string) error {
	// A map of strings and ints always marshals.
	data, _ := json.MarshalIndent(map[string]any{
		"address": srv.Address(), "port": srv.Port(), "pid": os.Getpid(), "version": Version,
		"startedAt": time.Now().Format(time.RFC3339), "tokenStore": store,
	}, "", "  ")
	return fsstore.WriteAtomic(path, append(data, '\n'), 0o600)
}

// buildCommit is the Commit make set, or else the one go build stamped in the binary; see commitOf.
func buildCommit() string {
	if Commit != "" {
		return Commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return commitOf(info.Settings)
}

// commitOf is the first 7 characters of vcs.revision, with "-dirty" when vcs.modified says the tree had uncommitted
// changes, and empty without a revision: outside a checkout, or with -buildvcs=false.
func commitOf(settings []debug.BuildSetting) string {
	var revision string
	var modified bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if revision == "" {
		return ""
	}
	revision = revision[:min(len(revision), 7)]
	if modified {
		revision += "-dirty"
	}
	return revision
}

// buildTime is the time make stamped, and zero when it stamped none or not in RFC 3339. It has no fallback: go build
// stamps only vcs.time, the time of the commit, which is not when the binary was built.
func buildTime(stamp string) time.Time {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}
	}
	return t
}

// acquireLock keeps a second daemon from running on the same data directory, with flock, which is syscall.Flock
// outside tests. Only a lock another process holds means that a daemon is running; any other failure is returned as
// it is, naming the lock file.
func acquireLock(path string, flock func(fd, how int) error) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() // the file was only opened, and held nothing yet
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another HarmFlow daemon is already running on this data directory")
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	// The pid in the file only tells a person which daemon holds the lock; the lock itself is the flock, so a pid that
	// cannot be written does not keep the daemon from running.
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

// findBuiltins locates the built-in agents, skills, and task templates: inside the app bundle, next to the binary, or in
// a checkout. The templates sit beside the agents and skills, which tell the directory apart.
func findBuiltins(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("HARMFLOW_BUILTINS_DIR"); v != "" {
		return v
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dir := filepath.Dir(exe)
	candidates := []string{filepath.Join(dir, "..", "Resources", "builtins"), filepath.Join(dir, "builtins")}
	for d := dir; d != "/" && d != "."; d = filepath.Dir(d) {
		candidates = append(candidates, d)
	}
	for _, c := range candidates {
		if holdsBuiltins(c) {
			abs, err := filepath.Abs(c)
			if err != nil {
				return c
			}
			return abs
		}
	}
	return ""
}

// holdsBuiltins reports whether dir has a built-in skill in its skills/ directory, a folder, or a built-in agent in its
// agents/ directory, a Markdown file, which tells the built-ins apart from any other directory named skills or agents.
func holdsBuiltins(dir string) bool {
	return holdsBuiltin(filepath.Join(dir, "skills"), func(e os.DirEntry) bool { return e.IsDir() }) ||
		holdsBuiltin(filepath.Join(dir, "agents"), func(e os.DirEntry) bool {
			return !e.IsDir() && strings.HasSuffix(e.Name(), ".md")
		})
}

// holdsBuiltin reports whether dir has an entry named with the built-in prefix that is the kind of item it holds.
func holdsBuiltin(dir string, isItem func(os.DirEntry) bool) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), app.BuiltinPrefix) && isItem(e) {
			return true
		}
	}
	return false
}

// userPath is the PATH harnesses and tools need: the user's login-shell PATH when it can be read, the daemon's own,
// and the usual install locations. A user service starts with a minimal PATH.
func userPath(home string) string {
	var parts []string
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, shell, "-lc", `printf %s "$PATH"`).Output(); err == nil {
		parts = append(parts, strings.Split(strings.TrimSpace(string(out)), ":")...)
	}
	parts = append(parts, strings.Split(os.Getenv("PATH"), ":")...)
	parts = append(parts, "/opt/homebrew/bin", "/usr/local/bin", filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".claude", "local"), "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, ":")
}

func withPath(env []string, path string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv)
		}
	}
	return append(out, "PATH="+path)
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
