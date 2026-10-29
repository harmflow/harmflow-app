package app

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// helperVariable makes the test binary stand in for a helper that a harness starts, such as gpg-agent, which is not an
// Apple platform binary, so KERN_PROCARGS2 returns its environment: "daemonize" starts the agent in a session of its
// own and exits, the first half of a double fork; "agent" records each line written to the FIFO APP_FIFO in the file
// APP_USES, for ten minutes at most. A harness's script sets the variable on the helper's command alone.
const helperVariable = "APP_HELPER"

// The App starts goroutines: its loops, each execution and its progress ticker, and harness detection. None may outlive
// the test that started it.
func TestMain(m *testing.M) {
	switch os.Getenv(helperVariable) {
	case "daemonize":
		// The agent gets a session of its own and /dev/null for its output; once this process exits, launchd adopts it.
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), helperVariable+"=agent")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if cmd.Start() != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "agent":
		ended := make(chan error, 1)
		go func() { ended <- agent(os.Getenv("APP_FIFO"), os.Getenv("APP_USES"), os.Getenv("APP_PIDFILE")) }()
		select {
		case <-ended:
			os.Exit(2)
		case <-time.After(10 * time.Minute):
			os.Exit(0)
		}
	}
	goleak.VerifyTestMain(m)
}

// agent appends each line written to the FIFO at fifo to the file uses. It writes its pid to pidFile, whole, once the
// FIFO is open, so from then on a writer's open finds a reader; it opens the FIFO for reading and writing, so its reads
// never end when a writer closes, and it returns only when a read or a write fails.
func agent(fifo, uses, pidFile string) error {
	f, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(uses, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return err
	}
	if err := os.Rename(pidFile+".tmp", pidFile); err != nil {
		return err
	}
	s := bufio.NewScanner(f)
	for s.Scan() {
		if _, err := out.WriteString(s.Text() + "\n"); err != nil {
			return err
		}
	}
	return s.Err()
}
