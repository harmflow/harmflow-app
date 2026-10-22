// Command harmflowd is the HarmFlow daemon: the scheduler, the executions, and the local API. It runs without the
// dashboard (ADR-001) and stops nothing when the dashboard closes.
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
	"github.com/harmflow/harmflow-app/daemon/internal/app"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// Version is set at build time; desktop and daemon always ship at the same version (D-080).
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
	builtinsFlag := flag.String("builtins", "", "directory holding the built-in agents/ and skills/")
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
	defer logFile.Close()
	var logOut io.Writer = logFile
	if isTerminal(os.Stderr) {
		logOut = io.MultiWriter(logFile, os.Stderr)
	}
	logger := log.New(logOut, "", log.LstdFlags)

	lock, err := acquireLock(filepath.Join(paths.Run, "daemon.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()

	pathEnv := userPath(userHome)
	harnessEnv := withPath(os.Environ(), pathEnv)

	secrets := tokenStore(paths, logger)
	token, err := secrets.Get()
	if errors.Is(err, secret.ErrNoToken) {
		if token, err = secret.NewToken(); err == nil {
			err = secrets.Set(token)
		}
	}
	if err != nil {
		logger.Printf("credential store %s unavailable (%v); using a user-only file", secrets.Kind(), err)
		secrets = secret.File{Path: filepath.Join(paths.Run, "token")}
		if token, err = secrets.Get(); errors.Is(err, secret.ErrNoToken) {
			if token, err = secret.NewToken(); err == nil {
				err = secrets.Set(token)
			}
		}
		if err != nil {
			return fmt.Errorf("token: %w", err)
		}
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
	exe, _ := os.Executable()
	daemonArgs := []string{}
	daemonEnv := map[string]string{"PATH": pathEnv}
	if homeFlag != "" || os.Getenv("HARMFLOW_HOME") != "" {
		daemonArgs = append(daemonArgs, "--home", home)
	}
	if paths.Builtins != "" {
		daemonArgs = append(daemonArgs, "--builtins", paths.Builtins)
	}
	if os.Getenv("HARMFLOW_TOKEN_STORE") != "" {
		daemonEnv["HARMFLOW_TOKEN_STORE"] = os.Getenv("HARMFLOW_TOKEN_STORE")
	}
	a, err := app.New(app.Deps{
		Paths: paths, Version: Version, Commit: buildCommit(), BuiltAt: buildTime(BuiltAt),
		Tasks: tasks, State: state, Executions: execs, Events: events, Validator: validator,
		Harnesses: []ports.HarnessAdapter{
			&claudecode.Adapter{Home: userHome, PathEnv: pathEnv, Env: harnessEnv},
			&codex.Adapter{Home: userHome, PathEnv: pathEnv, Env: harnessEnv},
		},
		Service:    launchd.NewAgent(userHome, filepath.Join(paths.Logs, "daemon.log")),
		System:     &sysinfo.Reader{},
		Tools:      &tools.Prober{Home: userHome, PathEnv: pathEnv, Env: harnessEnv},
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
	if err := a.Start(context.Background()); err != nil {
		return err
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	if err := writeRuntime(paths.RuntimeFile, srv, secrets.Kind()); err != nil {
		return err
	}
	defer os.Remove(paths.RuntimeFile)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	logger.Printf("stopped")
	return nil
}

func tokenStore(p app.Paths, logger *log.Logger) ports.SecretStore {
	if os.Getenv("HARMFLOW_TOKEN_STORE") == "file" {
		return secret.File{Path: filepath.Join(p.Run, "token")}
	}
	return secret.Keychain{Service: secret.KeychainService, Account: secret.KeychainAccount}
}

// writeRuntime publishes the address in a file only the user can read (D-090).
func writeRuntime(path string, srv *httpapi.Server, store string) error {
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

// acquireLock keeps a second daemon from running on the same data directory.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another HarmFlow daemon is already running on this data directory")
	}
	_ = f.Truncate(0)
	_, _ = f.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
	return f, nil
}

// findBuiltins locates the built-in agents and skills: inside the app bundle, next to the binary, or in a checkout.
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
		if st, err := os.Stat(filepath.Join(c, "skills")); err == nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(c, "agents")); err == nil {
				abs, _ := filepath.Abs(c)
				return abs
			}
		}
	}
	return ""
}

// userPath is the PATH harnesses and tools need: the user's login-shell PATH when it can be read, the daemon's own,
// and the usual install locations. A user service starts with a minimal PATH (ADR-009).
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
