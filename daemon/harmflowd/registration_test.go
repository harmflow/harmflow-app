package harmflowd

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// running is a daemon Run started for a test: its data directory, the address and token it published, and what Run
// returned once it stopped.
type running struct {
	home    string
	address string
	token   string
	done    chan error
}

// isolate gives Run a data directory and a home of its own, and keeps its token in a file rather than the Keychain.
func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARMFLOW_TOKEN_STORE", "file")
	t.Setenv("HARMFLOW_HOME", "")
	return t.TempDir()
}

// start runs the daemon with registrations on a goroutine, waits until it has published its address and started, and
// stops it when the test ends.
func start(t *testing.T, registrations ...Registration) *running {
	t.Helper()
	return startAt(t, isolate(t), registrations...)
}

// startAt is start on home, a data directory isolate gave, which the test may have filled beforehand.
func startAt(t *testing.T, home string, registrations ...Registration) *running {
	t.Helper()
	r := &running{home: home, done: make(chan error, 1)}
	go func() { r.done <- Run(Config{Home: home, Version: "test"}, registrations...) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, err := os.ReadFile(filepath.Join(home, "run", "daemon.json"))
		if err == nil {
			var runtime struct {
				Address string `json:"address"`
			}
			if err := json.Unmarshal(data, &runtime); err == nil && runtime.Address != "" {
				r.address = runtime.Address
				break
			}
		}
		select {
		case err := <-r.done:
			t.Fatalf("Run() = %v before the daemon published its address", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon published no address within 20 seconds")
		}
		time.Sleep(20 * time.Millisecond)
	}
	token, err := os.ReadFile(filepath.Join(home, "run", "token"))
	if err != nil {
		t.Fatal(err)
	}
	r.token = strings.TrimSpace(string(token))
	t.Cleanup(func() { r.stop(t) })
	// The address is published before the use cases have started, and every route but the health answers 503 until
	// they have.
	for {
		status, body := r.request(t, "GET", "/api/v1/health", true)
		var health struct {
			Status string `json:"status"`
		}
		if status == http.StatusOK && json.Unmarshal([]byte(body), &health) == nil && health.Status == "ok" {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /api/v1/health = %d %s 20 seconds after the start, want status ok", status, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// client asks without keeping a connection open, so no goroutine of it outlives the test.
var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}

// request sends method path to the daemon, with its token when withToken, and returns the status and the body.
func (r *running) request(t *testing.T, method, path string, withToken bool) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+r.address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withToken {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: reading the body: %v", method, path, err)
	}
	return res.StatusCode, string(body)
}

// stop asks the daemon to stop through the local API, as the dashboard does, and waits for Run to return nil.
func (r *running) stop(t *testing.T) {
	t.Helper()
	if status, body := r.request(t, "POST", "/api/v1/daemon/stop", true); status != 202 {
		t.Errorf("POST /api/v1/daemon/stop: status %d, body %q, want 202", status, body)
	}
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("Run() = %v once stopped, want nil", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run() did not return within 20 seconds of the stop request")
	}
}

// A registration's route is answered by the running daemon behind the local API's token, beside the API's own routes,
// and the registration is given the daemon's version, paths, and event stream.
func TestRegisteredRouteIsServedBehindTheToken(t *testing.T) {
	var given *Daemon
	register := func(d *Daemon) error {
		given = d
		d.Handle("GET /api/v1/registered", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"version": d.Version(), "home": d.Paths().Home})
		}))
		return nil
	}
	r := start(t, register)

	if status, _ := r.request(t, "GET", "/api/v1/registered", false); status != 401 {
		t.Errorf("GET /api/v1/registered without the token: status %d, want 401", status)
	}
	status, body := r.request(t, "GET", "/api/v1/registered", true)
	var got map[string]string
	if status != 200 || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("GET /api/v1/registered with the token: status %d, body %q, want 200 and JSON", status, body)
	}
	if got["version"] != "test" || got["home"] != r.home {
		t.Errorf("GET /api/v1/registered = %v, want version test and home %s", got, r.home)
	}
	if status, _ := r.request(t, "GET", "/api/v1/health", true); status != 200 {
		t.Errorf("GET /api/v1/health beside a registered route: status %d, want 200", status)
	}
	// The routes were taken at the start: one added later is not served.
	given.Handle("GET /api/v1/late", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {}))
	if status, _ := r.request(t, "GET", "/api/v1/late", true); status != 404 {
		t.Errorf("GET /api/v1/late, handled after the start: status %d, want 404", status)
	}

	p := given.Paths()
	if p.Home != r.home || p.Logs != filepath.Join(r.home, "logs") || p.RuntimeFile != filepath.Join(r.home, "run", "daemon.json") {
		t.Errorf("Daemon.Paths() = %+v, want the directories under %s", p, r.home)
	}
	events, _, cancel := given.Subscribe(0)
	if events == nil {
		t.Error("Daemon.Subscribe(0) gave no channel")
	}
	cancel()
}

// With nothing registered, the daemon starts as command harmflowd always has: its own routes answer, and no other.
func TestNothingRegisteredStartsTheDaemonAsBefore(t *testing.T) {
	r := start(t)
	if status, body := r.request(t, "GET", "/api/v1/health", true); status != 200 || !strings.Contains(body, `"version":"test"`) {
		t.Errorf("GET /api/v1/health: status %d, body %q, want 200 with version test", status, body)
	}
	if status, _ := r.request(t, "GET", "/api/v1/registered", true); status != 404 {
		t.Errorf("GET /api/v1/registered with nothing registered: status %d, want 404", status)
	}
}

// A registration that fails, or a route that cannot be added, stops the start: Run returns the error, and leaves
// nothing behind it running — no address published, the data directory's lock released, and no later registration
// called.
func TestARegistrationThatFailsStopsTheStart(t *testing.T) {
	failure := errors.New("the registration failed")
	ok := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {})
	cases := []struct {
		name          string
		registrations func(later *bool) []Registration
		want          error  // the error Run's must wrap, when there is one
		names         string // what Run's error must name
	}{
		{"an error", func(later *bool) []Registration {
			return []Registration{
				func(*Daemon) error { return failure },
				func(*Daemon) error { *later = true; return nil },
			}
		}, failure, "registration 1 of 2"},
		{"a route of the API's own", func(*bool) []Registration {
			return []Registration{func(d *Daemon) error { d.Handle("GET /api/v1/health", ok); return nil }}
		}, nil, `"GET /api/v1/health"`},
		{"a route outside the API", func(*bool) []Registration {
			return []Registration{func(d *Daemon) error { d.Handle("GET /registered", ok); return nil }}
		}, nil, `"GET /registered"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := isolate(t)
			var later bool
			done := make(chan error, 1)
			go func() { done <- Run(Config{Home: home, Version: "test"}, c.registrations(&later)...) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("Run() did not return within 20 seconds")
			}
			if err == nil {
				t.Fatal("Run() = nil, want the start to fail")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Errorf("Run() = %v, want it to wrap %v", err, c.want)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("Run() = %v, want it to name %s", err, c.names)
			}
			if later {
				t.Error("Run() called a registration after one failed")
			}
			if _, err := os.Stat(filepath.Join(home, "run", "daemon.json")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("after a failed start, run/daemon.json: %v, want none", err)
			}
			lock, err := acquireLock(filepath.Join(home, "run", "daemon.lock"), syscall.Flock)
			if err != nil {
				t.Fatalf("after a failed start, acquireLock() = %v, want the lock free", err)
			}
			lock.Close()
		})
	}
}

// A start of the use cases that fails, once the local API already serves, stops the start as a registration's error
// does: Run returns the error only once the local API has stopped, so nothing answers on its port, no address is
// published, and the data directory's lock is released.
func TestAStartThatFailsStopsTheLocalAPI(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Settings that are no JSON fail the start of the use cases, after the local API has begun to serve.
	if err := os.WriteFile(filepath.Join(home, "state", "settings.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The port is fixed beforehand, since the runtime file that publishes it is gone once Run returns.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	done := make(chan error, 1)
	go func() { done <- Run(Config{Home: home, Port: port, Version: "test"}) }()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run() did not return within 20 seconds")
	}
	if err == nil || !strings.Contains(err.Error(), "state settings") {
		t.Fatalf("Run() with settings that are no JSON = %v, want the error of loading the settings", err)
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		conn.Close()
		t.Errorf("after a failed start, dialing the local API at %s = a connection, want it refused", address)
	}
	if _, err := os.Stat(filepath.Join(home, "run", "daemon.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("after a failed start, run/daemon.json: %v, want none", err)
	}
	lock, err := acquireLock(filepath.Join(home, "run", "daemon.lock"), syscall.Flock)
	if err != nil {
		t.Fatalf("after a failed start, acquireLock() = %v, want the lock free", err)
	}
	lock.Close()
}
