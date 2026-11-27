// Command harmflowd is the HarmFlow daemon: the scheduler, the executions, and the local API. It runs without the
// dashboard and stops nothing when the dashboard closes. It starts the daemon through package harmflowd, with nothing
// registered.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"github.com/harmflow/harmflow-app/daemon/harmflowd"
	"github.com/harmflow/harmflow-app/daemon/internal/adapters/launchd"
)

// Version is set at build time; desktop and daemon always ship at the same version.
var Version = "0.2.0"

// Revision is the full commit make built the daemon from, set at build time: make reads it from git also in a git
// worktree, where go build stamps none.
var Revision = ""

// Modified is "true" when make built the daemon from a tree with uncommitted changes; see Revision.
var Modified = ""

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
	revision, dirty := buildRevision()
	cfg := harmflowd.Config{Home: *homeFlag, Builtins: *builtinsFlag, Port: *portFlag, Version: Version,
		Commit: shortCommit(revision, dirty), Revision: revision, Dirty: dirty, BuiltAt: buildTime(BuiltAt)}
	if err := harmflowd.Run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "harmflowd:", err)
		os.Exit(exitStatus(err, os.Getenv("XPC_SERVICE_NAME")))
	}
}

// exitStatus is the status harmflowd exits with once Run failed with err; service is $XPC_SERVICE_NAME. A daemon that
// launchd started as the login agent, and that found the data directory held by a running daemon, exits with 0: the
// agent's KeepAlive restarts only a job that failed, so launchd does not start it again every 10 seconds for as long
// as the other daemon runs (harmflow-app#978). launchd sets $XPC_SERVICE_NAME to the label of the job it starts, and a
// process inherits it from whatever started it, so only the agent's exact label counts. Any other failure exits with 1.
func exitStatus(err error, service string) int {
	if errors.Is(err, harmflowd.ErrAlreadyRunning) && service == launchd.Label {
		return 0
	}
	return 1
}

// buildRevision is the Revision and Modified make set, or else the ones go build stamped in the binary; see
// revisionOf. The short commit of GET /health and the full one of each execution's record both come from it, so the
// one is always a prefix of the other.
func buildRevision() (revision string, dirty bool) {
	if Revision != "" {
		return Revision, Modified == "true"
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	return revisionOf(info.Settings)
}

// revisionOf is vcs.revision, and whether vcs.modified says its tree had uncommitted changes; empty and false without
// a revision: outside a checkout, or with -buildvcs=false.
func revisionOf(settings []debug.BuildSetting) (revision string, dirty bool) {
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return "", false
	}
	return revision, dirty
}

// shortCommit is the commit Settings → About shows: the first 7 characters of revision, with "-dirty" when its tree
// had uncommitted changes, and empty without a revision.
func shortCommit(revision string, dirty bool) string {
	if revision == "" {
		return ""
	}
	short := revision[:min(len(revision), 7)]
	if dirty {
		short += "-dirty"
	}
	return short
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
