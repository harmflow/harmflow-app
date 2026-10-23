package httpapi

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// syncBuffer stands for daemon.log: the end of a minute writes to it from a timer's goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const (
	validToken     = "the-valid-token"
	presentedToken = "a-token-the-caller-presented"
)

var reasons = []string{reasonMissingToken, reasonInvalidToken, reasonForbiddenHost, reasonForbiddenOrigin}

// rejectedRequest is a request guard rejects for reason; accepted is "".
func rejectedRequest(reason, method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Host = "127.0.0.1:7000"
	r.Header.Set("Authorization", "Bearer "+validToken)
	switch reason {
	case reasonMissingToken:
		r.Header.Del("Authorization")
	case reasonInvalidToken:
		r.Header.Set("Authorization", "Bearer "+presentedToken)
	case reasonForbiddenHost:
		r.Host = "attacker.example"
	case reasonForbiddenOrigin:
		r.Header.Set("Origin", "https://example.com")
	}
	return r
}

type entry struct {
	method, path, reason, since string
	count                       int
}

var entryLine = regexp.MustCompile(`^local API rejected (\S+) ("(?:[^"\\]|\\.)*"): ([^(]+) \((\d+) requests? since (\S+)\)$`)

// entries reads the rejection entries out of the log, which holds nothing else here.
func entries(t *testing.T, out string) []entry {
	t.Helper()
	var es []entry
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		m := entryLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("not a rejection entry: %q", line)
		}
		path, err := strconv.Unquote(m[2])
		if err != nil {
			t.Fatalf("path %s: %v", m[2], err)
		}
		count, _ := strconv.Atoi(m[4])
		if _, err := time.Parse(time.RFC3339, m[5]); err != nil {
			t.Fatalf("since %s: %v", m[5], err)
		}
		es = append(es, entry{method: m[1], path: path, reason: m[3], count: count, since: m[5]})
	}
	return es
}

func guarded(s *Server) http.Handler {
	s.SetToken(validToken)
	return s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
}

func serve(t *testing.T, h http.Handler, r *http.Request, want int) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, want %d", r.Method, r.URL.Path, w.Code, want)
	}
}

func status(reason string) int {
	if reason == reasonMissingToken || reason == reasonInvalidToken {
		return 401
	}
	return 403
}

// FS-009 FR-003, AC-003: thousands of rejected requests add at most one entry per reason per minute, each with the
// count of the requests it stands for; an accepted request adds none.
func TestAFloodOfRejectionsIsBounded(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)}
	start := clock.Now().Format(time.RFC3339)
	var out syncBuffer
	s := &Server{Logger: log.New(&out, "", 0), Clock: clock}
	h := guarded(s)
	t.Cleanup(func() { s.Shutdown(context.Background()) })

	const flood = 5000
	for i := 0; i < flood; i++ {
		for _, reason := range reasons {
			serve(t, h, rejectedRequest(reason, "GET", "/api/v1/tasks"), status(reason))
		}
	}
	serve(t, h, rejectedRequest("", "GET", "/api/v1/tasks"), 204)
	got := entries(t, out.String())
	if len(got) != len(reasons) {
		t.Fatalf("%d entries after the flood, want one per reason:\n%s", len(got), out.String())
	}
	for i, e := range got {
		want := entry{method: "GET", path: "/api/v1/tasks", reason: reasons[i], count: 1, since: start}
		if e != want {
			t.Errorf("entry %d: %+v, want %+v", i, e, want)
		}
	}

	clock.Advance(59 * time.Second)
	for _, reason := range reasons {
		serve(t, h, rejectedRequest(reason, "POST", "/api/v1/tasks/x/run"), status(reason))
	}
	if n := len(entries(t, out.String())); n != len(reasons) {
		t.Fatalf("%d entries within the minute, want %d:\n%s", n, len(reasons), out.String())
	}

	clock.Advance(time.Second)
	for _, reason := range reasons {
		serve(t, h, rejectedRequest(reason, "DELETE", "/api/v1/tasks/x"), status(reason))
	}
	got = entries(t, out.String())
	if len(got) != 2*len(reasons) {
		t.Fatalf("%d entries a minute later, want %d:\n%s", len(got), 2*len(reasons), out.String())
	}
	for i, e := range got[len(reasons):] {
		// The flood's other requests, the one within the minute, and this one; named by the last.
		want := entry{method: "DELETE", path: "/api/v1/tasks/x", reason: reasons[i], count: flood + 1, since: start}
		if e != want {
			t.Errorf("entry %d: %+v, want %+v", len(reasons)+i, e, want)
		}
	}
	if strings.Contains(out.String(), presentedToken) {
		t.Errorf("the log holds the presented token:\n%s", out.String())
	}
}

// The requests a minute's entry has not counted yet are written when the minute ends, and when the daemon stops.
func TestPendingRejectionsAreWritten(t *testing.T) {
	var out syncBuffer
	s := &Server{Logger: log.New(&out, "", 0), RejectionWindow: 100 * time.Millisecond}
	h := guarded(s)

	first := time.Now()
	for i := 0; i < 50; i++ {
		serve(t, h, rejectedRequest(reasonInvalidToken, "GET", "/api/v1/tasks"), 401)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(entries(t, out.String())) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := entries(t, out.String())
	if len(got) != 2 || got[0].count != 1 || got[1].count != 49 {
		t.Fatalf("entries %+v, want the first request and then the other 49:\n%s", got, out.String())
	}
	if elapsed := time.Since(first); elapsed < s.RejectionWindow {
		t.Errorf("the second entry came %v after the first, within the window", elapsed)
	}

	for i := 0; i < 3; i++ {
		serve(t, h, rejectedRequest(reasonForbiddenOrigin, "GET", "/api/v1/tasks"), 403)
		serve(t, h, rejectedRequest(reasonInvalidToken, "GET", "/api/v1/tasks"), 401)
	}
	s.Shutdown(context.Background())
	got = entries(t, out.String())
	counts := map[string]int{}
	for _, e := range got {
		counts[e.reason] += e.count
	}
	if counts[reasonInvalidToken] != 53 || counts[reasonForbiddenOrigin] != 3 {
		t.Errorf("counts %v after stopping, want every request counted:\n%s", counts, out.String())
	}
}

// A path is the caller's text: it is quoted, so it cannot forge an entry, and shortened, so it cannot fill the log.
func TestARejectionEntryIsOneBoundedLine(t *testing.T) {
	var out syncBuffer
	s := &Server{Logger: log.New(&out, "", 0)}
	h := guarded(s)
	forged := "/api/v1/x%0Alocal%20API%20rejected%20GET%20%22/y%22:%20missing%20token%20(1%20request%20since%20now)"
	serve(t, h, rejectedRequest(reasonForbiddenHost, "GET", forged), 403)
	serve(t, h, rejectedRequest(reasonForbiddenOrigin, "GET", "/"+strings.Repeat("a", 64<<10)), 403)
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2:\n%s", len(lines), out.String())
	}
	got := entries(t, out.String())
	if !strings.HasPrefix(got[0].path, "/api/v1/x\nlocal API rejected") {
		t.Errorf("path %q", got[0].path)
	}
	if len(lines[1]) > 1024 {
		t.Errorf("an entry of %d bytes", len(lines[1]))
	}
}
