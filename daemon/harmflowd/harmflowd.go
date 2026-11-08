// Package harmflowd starts the HarmFlow daemon: the scheduler, the executions, and the local API. Command harmflowd
// runs it with nothing registered. A build that adds to the daemon imports this package from a main of its own and
// passes its registrations to Run, which is the daemon's registration point: a change to Run, Config, Registration,
// Daemon, Paths, or Event is a breaking change.
package harmflowd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// Config is what the daemon is started with: what command harmflowd reads from its flags, and what its build stamped.
// Its zero value starts the daemon with its defaults and no version. Autostart starts the running executable again with
// the flags --home and --builtins when Home, $HARMFLOW_HOME, or the built-ins directory is set, so a main of its own
// that calls Run reads those two flags into Home and Builtins, as command harmflowd does.
type Config struct {
	// Home is HarmFlow's data directory; empty, it is $HARMFLOW_HOME, or ~/Library/Application Support/HarmFlow.
	Home string
	// Builtins is the directory holding the built-in agents/, skills/, and templates/; empty, it is
	// $HARMFLOW_BUILTINS_DIR, or the one the daemon finds beside its executable.
	Builtins string
	// Port is the port the local API listens on; 0 picks a free one.
	Port int
	// Version is the daemon's version; the desktop and the daemon always ship at the same version.
	Version string
	// Commit is the short commit the daemon was built from; empty when it is not known.
	Commit string
	// BuiltAt is when the daemon was built; zero when it is not known.
	BuiltAt time.Time
}

// Registration adds to the daemon as it starts. Run calls each registration once, in the order given, after the daemon
// has opened its data directory and built its use cases, and before it listens or runs anything. An error stops the
// start: Run returns it, with nothing left running.
type Registration func(*Daemon) error

// Daemon is the daemon as a registration sees it while it starts. A registration may keep it, and its handlers may call
// it while the daemon runs, Handle excepted.
type Daemon struct {
	app    *app.App
	routes []httpapi.Route
	// started is set once Run has called every registration; Handle adds no route from then on.
	started atomic.Bool
}

// Paths are the directories of one installation.
type Paths struct {
	// Home is HarmFlow's data directory; every other directory but Builtins and UserHome is under it.
	Home string
	// Tasks holds the task documents.
	Tasks string
	// State holds the daemon's state.
	State string
	// Executions holds the execution records.
	Executions string
	// Logs holds daemon.log and the executions' event logs.
	Logs string
	// Workspace holds the executions' working directories.
	Workspace string
	// Runs holds what each execution leaves for the harness it runs.
	Runs string
	// Run holds the files of the running daemon: its lock, and RuntimeFile.
	Run string
	// Builtins is the directory of the built-in agents, skills, and task templates; empty when none was found.
	Builtins string
	// RuntimeFile is the file that publishes the running daemon's address.
	RuntimeFile string
	// UserHome is the user's home directory.
	UserHome string
}

// Event is one event of the daemon's stream, as the local API's event stream sends it.
type Event = domain.Event

// Paths are the installation's directories.
func (d *Daemon) Paths() Paths {
	p := d.app.Paths()
	return Paths{Home: p.Home, Tasks: p.Tasks, State: p.State, Executions: p.Executions, Logs: p.Logs,
		Workspace: p.Workspace, Runs: p.Runs, Run: p.Run, Builtins: p.Builtins, RuntimeFile: p.RuntimeFile,
		UserHome: p.UserHome}
}

// Version is the daemon's version, Config.Version.
func (d *Daemon) Version() string { return d.app.Version() }

// Subscribe streams the daemon's events after the id after, as the local API's event stream does: the buffered ones
// first, then the live ones. replayGap reports that the buffer no longer reaches back to after. cancel ends the
// subscription; a subscriber that falls behind has its channel closed.
func (d *Daemon) Subscribe(after int64) (events <-chan Event, replayGap bool, cancel func()) {
	return d.app.Subscribe(after)
}

// Handle adds a route to the local API, behind the same Host, Origin, and token checks as every other route. pattern is
// an http.ServeMux pattern whose path is under /api/v1/, such as "GET /api/v1/example/{id}". A pattern outside
// /api/v1/, one http.ServeMux refuses, or one that conflicts with another route stops the start, as a registration's
// error does. The routes are taken while the registrations run: once Run has called the last one, Handle adds nothing.
// Handle belongs to the registration's own call, on its goroutine; another goroutine must not call it meanwhile.
func (d *Daemon) Handle(pattern string, handler http.Handler) {
	if d.started.Load() {
		return
	}
	d.routes = append(d.routes, httpapi.Route{Pattern: pattern, Handler: handler})
}

// Run starts the daemon with registrations, and returns once it has stopped: on SIGINT or SIGTERM, at the user's
// request through the local API, or when the local API stops. It returns an error when the daemon cannot start, a
// registration's included, or when the local API stops on one.
func Run(cfg Config, registrations ...Registration) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home := firstNonEmpty(cfg.Home, os.Getenv("HARMFLOW_HOME"), filepath.Join(userHome, "Library", "Application Support", "HarmFlow"))
	paths := app.NewPaths(home, userHome, findBuiltins(cfg.Builtins))
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
	if cfg.Home != "" || os.Getenv("HARMFLOW_HOME") != "" {
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
	// An agent an earlier version wrote for this daemon takes what Enable writes now from the next login. The daemon
	// goes on whatever the rewrite does: it only changes what the next login starts.
	service := launchd.NewAgent(userHome, filepath.Join(paths.Logs, "daemon.log"))
	if exe != "" {
		if rewritten, err := service.Refresh(exe, daemonArgs, daemonEnv); err != nil {
			logger.Printf("bringing the login agent up to date: %v", err)
		} else if rewritten {
			logger.Printf("brought the login agent %s up to date; it applies from the next login", service.Location())
		}
	}
	system := sysinfo.New(paths.Home)
	a, err := app.New(app.Deps{
		Paths: paths, Version: cfg.Version, Commit: cfg.Commit, BuiltAt: cfg.BuiltAt, Wake: wake.NewMonitor(),
		Tasks: tasks, State: state, Executions: execs, Events: events, Validator: validator,
		Harnesses: []ports.HarnessAdapter{
			&claudecode.Adapter{Home: userHome, PathEnv: pathEnv, Env: harnessEnv},
			&codex.Adapter{Home: userHome, PathEnv: pathEnv, Env: harnessEnv, ClientVersion: cfg.Version},
		},
		Service:    service,
		System:     system,
		Tools:      &tools.Prober{Home: userHome, PathEnv: pathEnv, Env: harnessEnv},
		Workspace:  &workspace.Reader{PathEnv: pathEnv, Env: harnessEnv},
		HarnessEnv: harnessEnv, DaemonExecutable: exe, DaemonArgs: daemonArgs, DaemonEnv: daemonEnv, Logger: logger,
		SignInTimeout: signInTimeout(logger),
		HarnessTokens: harnessTokens(),
	})
	if err != nil {
		return err
	}
	// The registrations run before the local API listens and before the use cases start, so one that fails leaves
	// nothing running.
	d := &Daemon{app: a}
	for i, register := range registrations {
		if err := register(d); err != nil {
			return fmt.Errorf("registration %d of %d: %w", i+1, len(registrations), err)
		}
	}
	d.started.Store(true)
	srv := &httpapi.Server{App: a, Secrets: secrets, NewToken: secret.NewToken, Logger: logger, Routes: d.routes}
	srv.SetToken(token)
	if err := srv.Prepare(); err != nil {
		return err
	}
	if err := srv.Listen(cfg.Port); err != nil {
		return err
	}
	// The daemon's own context: what Start runs under ends when Run returns, after Stop.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	system.Start(ctx)
	defer system.Stop()
	if err := a.Start(ctx); err != nil {
		return err
	}
	// Taken before the address is published, so a signal sent to a daemon that answers is never the default action of
	// the runtime, which ends the process with SIGQUIT's goroutine dump.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(sig)
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	if err := writeRuntime(paths.RuntimeFile, srv, secrets.Kind(), cfg.Version); err != nil {
		return err
	}
	defer os.Remove(paths.RuntimeFile) // a file left behind is replaced at the next start
	logger.Printf("HarmFlow daemon %s listening on %s (home %s, builtins %s)", cfg.Version, srv.Address(), home, paths.Builtins)

	if err := awaitStop(a, a.ShutdownRequested, sig, serveErr, logger); err != nil {
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

// stopper is what awaitStop asks of the use cases.
type stopper interface {
	RequestDrain(deadline time.Duration) error
	ShutdownReason() domain.Text
	Stop(reason domain.Text)
}

// awaitStop waits for what stops the daemon, and stops it. SIGINT and SIGTERM stop it at once, cancelling the running
// executions. SIGQUIT asks for a draining stop with the default deadline, and the wait goes on, so a SIGINT or a
// SIGTERM still stops the daemon at once during the drain. requested closing — a stop the local API asked for, or the
// end of a drain — stops it for the reason the use cases give. A local API that stops serving stops it too, and its
// error is returned.
func awaitStop(a stopper, requested <-chan struct{}, sig <-chan os.Signal, serveErr <-chan error, logger *log.Logger) error {
	for {
		select {
		case s := <-sig:
			if s == syscall.SIGQUIT {
				logger.Printf("draining on %s", s)
				if err := a.RequestDrain(0); err != nil {
					logger.Printf("draining on %s: %v", s, err)
				}
				continue
			}
			logger.Printf("stopping on %s", s)
			a.Stop(domain.T("daemon_signal", "the daemon received {{signal}}", "signal", s.String()))
			return nil
		case <-requested:
			reason := a.ShutdownReason()
			logger.Printf("stopping: %s", reason.Message)
			time.Sleep(200 * time.Millisecond) // let the API answer the stop request
			a.Stop(reason)
			return nil
		case err := <-serveErr:
			a.Stop(domain.T("local_api_stopped", "the local API stopped"))
			return err
		}
	}
}

// writeRuntime publishes the address in a file only the user can read.
func writeRuntime(path string, srv *httpapi.Server, store, version string) error {
	// A map of strings and ints always marshals.
	data, _ := json.MarshalIndent(map[string]any{
		"address": srv.Address(), "port": srv.Port(), "pid": os.Getpid(), "version": version,
		"startedAt": time.Now().Format(time.RFC3339), "tokenStore": store,
	}, "", "  ")
	return fsstore.WriteAtomic(path, append(data, '\n'), 0o600)
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

// harnessTokens is where a harness's long-lived token is kept: an item of the Keychain the local API token's is in,
// under harnessTokenService and the harness's id. A daemon whose token store is a file keeps none, since the token
// belongs in the operating system's credential store alone.
func harnessTokens() func(string) ports.SecretStore {
	if os.Getenv("HARMFLOW_TOKEN_STORE") == "file" {
		return nil
	}
	keychain := os.Getenv("HARMFLOW_KEYCHAIN")
	return func(harness string) ports.SecretStore {
		return secret.Keychain{Service: harnessTokenService, Account: harness, Path: keychain}
	}
}

// harnessTokenService names the Keychain items of the harnesses' long-lived tokens.
const harnessTokenService = "com.harmflow.harness-token"

// signInTimeoutVariable shortens how long a harness's sign-in waits for the browser, as the tests that let one time out
// need; a duration Go reads, such as 5s. Unset, the use cases' default holds.
const signInTimeoutVariable = "HARMFLOW_SIGN_IN_TIMEOUT"

// signInTimeout reads signInTimeoutVariable; a value that is not a positive duration is logged and leaves the default.
func signInTimeout(logger *log.Logger) time.Duration {
	v := os.Getenv(signInTimeoutVariable)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		logger.Printf("%s=%q is not a positive duration; the sign-in waits its default", signInTimeoutVariable, v)
		return 0
	}
	return d
}

// findBuiltins locates the built-in agents, skills, and task templates: inside the app bundle, next to the binary, or
// in a checkout. The templates sit beside the agents and skills, which tell the directory apart.
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
