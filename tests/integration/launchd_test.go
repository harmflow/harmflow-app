package integration

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// launchdEnv opts in to the test that has the user's own launchd start a daemon in the signed-in user's GUI domain.
const launchdEnv = "HARMFLOW_TEST_LAUNCHD"

// userAgentLabel is the label of the user agent HarmFlow writes: the user's own job, which no test loads, replaces,
// stops, or unloads.
const userAgentLabel = "com.harmflow.daemon"

// realLaunchd skips the test unless it runs on macOS with HARMFLOW_TEST_LAUNCHD exactly 1, in a signed-in user session
// whose GUI domain launchd serves, and otherwise returns that domain, gui/<uid>.
func realLaunchd(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || os.Getenv(launchdEnv) != "1" {
		t.Skipf("needs macOS, a signed-in user session, and %s=1: launchd starts a daemon in the user's session",
			launchdEnv)
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if err := exec.Command("/bin/launchctl", "print", domain).Run(); err != nil {
		t.Skipf("needs a signed-in user session: launchctl print %s: %v", domain, err)
	}
	return domain
}

// launchdPIDLine is the line of launchctl print that names the pid of a job's running process.
var launchdPIDLine = regexp.MustCompile(`(?m)^\s*pid = (\d+)$`)

// launchdJob reads launchd's own record of the job with the label in the domain: whether it is loaded, and the pid of
// its running process, 0 when none runs.
func launchdJob(domain, label string) (loaded bool, pid int) {
	out, err := exec.Command("/bin/launchctl", "print", domain+"/"+label).Output()
	if err != nil {
		return false, 0
	}
	if m := launchdPIDLine.FindSubmatch(out); m != nil {
		pid, _ = strconv.Atoi(string(m[1]))
	}
	return true, pid
}

// plistJSON reads a property list as JSON, through plutil.
func plistJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	out, err := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", path).Output()
	if err != nil {
		t.Fatalf("plutil -convert json %s: %v", path, err)
	}
	var p map[string]any
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatalf("plutil -convert json %s printed no object: %v", path, err)
	}
	return p
}

// launchAgents lists the files of a LaunchAgents folder with their size and modification time; an absent folder lists
// none.
func launchAgents(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = fmt.Sprintf("%d bytes, modified %s", info.Size(), info.ModTime().Format(time.RFC3339Nano))
	}
	return files
}

// The daemon launchd starts from the user agent HarmFlow writes runs in the user's context, without elevated
// privileges, as one started by hand does (TestDaemonRunsAsTheUser). A daemon of the test's own, on a home and a HOME
// of its own, writes its agent when autostart is turned on through the local API. The test stops it, copies the agent
// with only its label changed to one of the run's own, and has launchd load the copy into the user's GUI domain, as a
// login loads the agent, so launchd starts harmflowd with the arguments HarmFlow wrote. The user's own agent, its job,
// and ~/Library/LaunchAgents stay as they are, and the job the test loaded is booted out whether it passes or fails.
func TestLaunchdStartedDaemonRunsAsTheUser(t *testing.T) {
	domain := realLaunchd(t)
	if os.Getuid() == 0 {
		t.Fatal("the test process runs as root, so a daemon that keeps its user still runs with elevated privileges: " +
			"run the tests as a user that is not root")
	}
	ownHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ownAgentsDir := filepath.Join(ownHome, "Library", "LaunchAgents")
	ownAgents := launchAgents(t, ownAgentsDir)
	ownLoaded, ownPID := launchdJob(domain, userAgentLabel)
	// Registered first, so it runs last, once the test's own job is booted out.
	t.Cleanup(func() {
		if got := launchAgents(t, ownAgentsDir); !maps.Equal(got, ownAgents) {
			t.Errorf("%s changed during the test: %v, was %v", ownAgentsDir, got, ownAgents)
		}
		if loaded, pid := launchdJob(domain, userAgentLabel); loaded != ownLoaded || pid != ownPID {
			t.Errorf("the user's own job %s reads loaded %v, pid %d, was loaded %v, pid %d", userAgentLabel, loaded, pid,
				ownLoaded, ownPID)
		}
	})

	home, userHome := t.TempDir(), t.TempDir()
	// The daemon's HOME alone: its user agent lies under the throwaway HOME, never in the user's own LaunchAgents.
	d := startDaemon(t, home, "HOME="+userHome)
	agent := filepath.Join(userHome, "Library", "LaunchAgents", userAgentLabel+".plist")
	a, _ := d.call("PATCH", "/settings", map[string]any{"autostart": true}, 200)["autostart"].(map[string]any)
	if a["enabled"] != true || a["mechanism"] != "launchd" || a["plistPath"] != agent {
		t.Fatalf("PATCH /settings {autostart: true} reads autostart %v, want on, through launchd, at %s", a, agent)
	}
	written := plistJSON(t, agent)
	// launchd gives the job the user's own HOME, not the throwaway one: the agent's --home and its file token store keep
	// the daemon it starts on the test's home, away from the user's own HarmFlow home and login keychain.
	args, _ := written["ProgramArguments"].([]any)
	if i := slices.Index(args, any("--home")); i < 1 || i+1 >= len(args) || args[i+1] != home {
		t.Fatalf("the agent's ProgramArguments are %v, want --home %s", args, home)
	}
	if env, _ := written["EnvironmentVariables"].(map[string]any); env["HARMFLOW_TOKEN_STORE"] != "file" {
		t.Fatalf("the agent's EnvironmentVariables are %v, want HARMFLOW_TOKEN_STORE=file", env)
	}
	d.stop()
	if err := os.Remove(filepath.Join(home, "run", "daemon.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	label := fmt.Sprintf("com.harmflow.test.launchd-%d-%d", os.Getpid(), time.Now().UnixNano())
	copied := filepath.Join(userHome, "Library", "LaunchAgents", label+".plist")
	data, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/plutil", "-replace", "Label", "-string", label, copied).CombinedOutput(); err != nil {
		t.Fatalf("plutil -replace Label %s: %v\n%s", copied, err, out)
	}
	want := maps.Clone(written)
	want["Label"] = label
	if got := plistJSON(t, copied); !reflect.DeepEqual(got, want) {
		t.Fatalf("the copied agent reads %v, want the agent HarmFlow wrote with only its Label changed: %v", got, want)
	}

	var pid int
	t.Cleanup(func() {
		if out, err := exec.Command("/bin/launchctl", "bootout", domain+"/"+label).CombinedOutput(); err != nil {
			if loaded, _ := launchdJob(domain, label); loaded {
				t.Errorf("launchctl bootout %s/%s: %v\n%s", domain, label, err, out)
			}
		}
		for deadline := time.Now().Add(10 * time.Second); pid > 0 && syscall.Kill(pid, 0) == nil; time.Sleep(100 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Errorf("the daemon launchd started, pid %d, still runs after its job was booted out", pid)
				_ = syscall.Kill(pid, syscall.SIGKILL)
				return
			}
		}
	})
	if out, err := exec.Command("/bin/launchctl", "bootstrap", domain, copied).CombinedOutput(); err != nil {
		t.Fatalf("launchctl bootstrap %s %s: %v\n%s", domain, copied, err, out)
	}
	for deadline := time.Now().Add(runtimeWait); pid == 0; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("launchd started no process for %s/%s within %v\nthe daemon's log ends:\n%s", domain, label,
				runtimeWait, daemonLogTail(home))
		}
		_, pid = launchdJob(domain, label)
	}
	base, token := awaitRuntime(home, runtimeWait)
	if base == "" {
		t.Fatalf("the daemon launchd started, pid %d, wrote no runtime file within %v\nits log ends:\n%s", pid,
			runtimeWait, daemonLogTail(home))
	}
	started := &daemon{t: t, home: home, base: base, token: token}
	if got, _ := started.call("GET", "/health", nil, 200)["pid"].(float64); int(got) != pid {
		t.Fatalf("GET /health of the daemon launchd started names the pid %v, want %d, launchd's own", got, pid)
	}
	wantUserIDs(t, pid)
}
