package eventlog

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

func TestRedact(t *testing.T) {
	in := "token ghp_" + strings.Repeat("a", 36) + " and Authorization: Bearer " + strings.Repeat("x", 30) +
		" and sk-ant-" + strings.Repeat("b", 30)
	out := Redact(in)
	if strings.Contains(out, "ghp_a") || strings.Contains(out, "xxxxxxxx") || strings.Contains(out, "sk-ant-bbb") {
		t.Errorf("secret survived: %s", out)
	}
	if !strings.Contains(out, "Authorization: Bearer [REDACTED]") {
		t.Errorf("header prefix lost: %s", out)
	}

	// Passwords, cookies, and session values are masked where a key and its separator name them; the same words in
	// ordinary text, or inside another word, are left as they are (FS-001 FR-006, FS-009 FR-005).
	for _, c := range []struct{ in, want string }{
		{"Sign in with password: hunter2 today", "Sign in with password: [REDACTED] today"},
		{"PASSWORD=hunter2", "PASSWORD=[REDACTED]"},
		{"passwd:s3cret", "passwd:[REDACTED]"},
		{"mysql --pwd=s3cret db", "mysql --pwd=[REDACTED] db"},
		{`password: "hunter2"`, `password: "[REDACTED]"`},
		{`"use password='hunter2' once"`, `"use password='[REDACTED]' once"`},
		{"Cookie: sessionid=abc123def456; theme=dark", "Cookie: [REDACTED]"},
		{"Set-Cookie: id=a3fWa; Expires=Wed, 21 Oct 2026 07:28:00 GMT; HttpOnly",
			"Set-Cookie: [REDACTED]"},
		{`curl -H "Cookie: sid=abc123" https://example.test`, `curl -H "Cookie: [REDACTED]" https://example.test`},
		{"sessionid=abc123def456", "sessionid=[REDACTED]"},
		{"Session: 9f8e7d6c", "Session: [REDACTED]"},
		{"SID=abc123; theme=dark", "SID=[REDACTED]; theme=dark"},
		{"https://example.test/cb?sid=abc123&next=/home", "https://example.test/cb?sid=[REDACTED]&next=/home"},

		{"password:\nthe next line", "password:\nthe next line"},
		{"Fill in the password field, then submit.", "Fill in the password field, then submit."},
		{"passwords: rotated monthly", "passwords: rotated monthly"},
		{"run pwd to print the directory", "run pwd to print the directory"},
		{"OLDPWD=/tmp/old", "OLDPWD=/tmp/old"},
		{"cookies: 3, and the Cookie jar is empty", "cookies: 3, and the Cookie jar is empty"},
		{"sessionContext: the previous attempt", "sessionContext: the previous attempt"},
		{"the session started at 10:00", "the session started at 10:00"},
		{"session 0b5c-77 on model sonnet", "session 0b5c-77 on model sonnet"},
		{"This session is run by HarmFlow, a local scheduler.", "This session is run by HarmFlow, a local scheduler."},
		{"inside=1, side: left, consider=yes", "inside=1, side: left, consider=yes"},
	} {
		t.Run(c.in, func(t *testing.T) {

			out := Redact(c.in)
			if out != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, out, c.want)
			}
			// A capture is redacted as it is written and again as it is read.
			if again := Redact(out); again != out {
				t.Errorf("Redact(%q) = %q, want it unchanged: redacting is idempotent", out, again)
			}
		})
	}
}

// The agent's and the harness's text reaches the daemon as structs, so every string in one is redacted in place, however
// deep it sits; numbers, times, and unexported fields are left alone (FS-009 FR-005, FR-007).
func TestRedactFields(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	type kind string
	type inner struct {
		Text   string
		Params map[string]string
	}
	type outer struct {
		Message string
		Kind    kind
		Inner   inner
		Ptr     *inner
		Nil     *inner
		List    []inner
		Strings []string
		Payload map[string]any
		When    time.Time
		Count   int
		hidden  string
	}
	v := outer{Message: "pushed with " + token, Kind: kind(token),
		Inner: inner{Text: token, Params: map[string]string{"detail": token}}, Ptr: &inner{Text: token},
		List: []inner{{Text: token}}, Strings: []string{token},
		Payload: map[string]any{"nested": map[string]any{"text": token}, "list": []any{token, 3}, "count": 3},
		When:    time.Unix(1, 0), Count: 7, hidden: token}
	if !RedactFields(&v) {
		t.Error("it reported nothing redacted")
	}
	data, _ := json.Marshal(v)
	if strings.Contains(string(data), "ghp_") {
		t.Errorf("a secret survived: %s", data)
	}
	if v.Message != "pushed with [REDACTED]" || v.Payload["count"] != 3 || v.Count != 7 || !v.When.Equal(time.Unix(1, 0)) ||
		v.hidden != token {
		t.Errorf("redacted more or less than the strings: %+v", v)
	}
	clean := outer{Message: "nothing to hide", Payload: map[string]any{"text": "plain"}}
	if RedactFields(&clean) || clean.Message != "nothing to hide" {
		t.Errorf("it reported a change in clean text: %+v", clean)
	}
}

// A text's params quote a harness's output, so they are redacted as the message is.
func TestAppendRedactsParams(t *testing.T) {
	l, err := New(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	token := "ghp_" + strings.Repeat("a", 36)
	text := domain.T("command_failed", "{{command}} failed: {{detail}}", "command", "gh", "detail", token)
	logged := l.Append(domain.Event{Type: domain.EventHarnessExited, ExecutionID: "e1"}.Says(text))
	if strings.Contains(logged.Message, token) || strings.Contains(logged.Params["detail"], token) {
		t.Errorf("message %q, params %v", logged.Message, logged.Params)
	}
	logged = l.Append(domain.Event{Type: domain.EventHarnessExited, ExecutionID: "e1",
		Payload: domain.ErrorPayload(map[string]any{}, text)})
	if p, _ := logged.Payload["errorParams"].(map[string]string); p == nil || strings.Contains(p["detail"], token) {
		t.Errorf("payload %v", logged.Payload)
	}
}

// FS-008 FR-008: an attempt's capture reads back redacted, its end from the start of a line when it is too long.
func TestReadRaw(t *testing.T) {
	l, err := New(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	w, err := l.RawWriter("e1", 2)
	if err != nil {
		t.Fatal(err)
	}
	token := "ghp_" + strings.Repeat("a", 36)
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk\n-----END OPENSSH PRIVATE KEY-----\n"
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ { // stdout and stderr write at the same time
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = w.Write([]byte("line " + token + "\n"))
			}
		}()
	}
	wg.Wait()
	_, _ = w.Write([]byte(key + "last line"))
	_ = w.Close()

	c, err := l.ReadRaw("e1", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Each line is redacted as it is written; the key spans lines, so only reading the text whole masks it.
	if c.Offset != 0 || c.Text != strings.Repeat("line [REDACTED]\n", 200)+"[REDACTED]\nlast line" {
		t.Errorf("whole capture: offset %d, text ends %q", c.Offset, c.Text[max(0, len(c.Text)-60):])
	}

	// The last 100 bytes start inside a line, so the text starts at the next one: the key's.
	tail, err := l.ReadRaw("e1", 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if tail.Size != c.Size || tail.Offset != c.Size-int64(len(key+"last line")) || tail.Text != "[REDACTED]\nlast line" {
		t.Errorf("tail: size %d of %d, offset %d, text %q", tail.Size, c.Size, tail.Offset, tail.Text)
	}

	if none, err := l.ReadRaw("e1", 1, 0); err != nil || none != (ports.RawCapture{}) {
		t.Errorf("an attempt with no capture: %+v %v", none, err)
	}
	if err := l.DeleteLog("e1"); err != nil {
		t.Fatal(err)
	}
	if gone, err := l.ReadRaw("e1", 2, 0); err != nil || gone.Size != 0 {
		t.Errorf("a deleted capture: %+v %v", gone, err)
	}
}

// FS-008 FR-007: one JSONL per execution, replay after an id, and live delivery with a backlog after an id.
func TestAppendReplaySubscribe(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	first := l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventExecutionStarted})
	second := l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventToolStarted, Message: "ghp_" + strings.Repeat("z", 36)})
	l.Append(domain.Event{ExecutionID: "e2", Type: domain.EventExecutionStarted})
	if second.ID <= first.ID {
		t.Fatal("ids are not increasing")
	}
	events, expired, err := l.Replay("e1", first.ID, 0)
	if err != nil || expired || len(events) != 1 || events[0].ID != second.ID {
		t.Fatalf("replay: %v %v %+v", err, expired, events)
	}
	if strings.Contains(events[0].Message, "zzzz") {
		t.Error("the stored event was not redacted")
	}
	ch, gap, cancel := l.Subscribe(first.ID)
	defer cancel()
	if gap {
		t.Error("unexpected gap")
	}
	if e := <-ch; e.ID != second.ID {
		t.Errorf("backlog starts at %d, want %d", e.ID, second.ID)
	}
	live := l.Append(domain.Event{Type: domain.EventDaemonStarted})
	for e := range ch {
		if e.ID == live.ID {
			break
		}
	}
	_, gap, cancel2 := l.Subscribe(1)
	cancel2()
	if !gap {
		t.Error("an id older than the buffer must report a gap")
	}
	if err := l.DeleteLog("e1"); err != nil {
		t.Fatal(err)
	}
	if _, expired, _ := l.Replay("e1", 0, 0); !expired {
		t.Error("a deleted log must read as expired")
	}
	if _, err := os.Stat(dir + "/e2.jsonl"); err != nil {
		t.Error("another execution's log was touched")
	}
}

// A subscriber that stops reading never holds up Append: once its buffer is full it is dropped, its channel closed
// after the events it holds, and a new subscription replays from where it stopped.
func TestSlowSubscriberIsDropped(t *testing.T) {
	l, err := New(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ch, _, cancel := l.Subscribe(0)
	defer cancel()
	appended := make(chan domain.Event)
	go func() {
		var last domain.Event
		for range subscriberBuffer + 10 {
			last = l.Append(domain.Event{Type: domain.EventDaemonStarted})
		}
		appended <- last
	}()
	var last domain.Event
	select {
	case last = <-appended:
	case <-time.After(10 * time.Second):
		t.Fatal("Append waited for a subscriber that does not read")
	}
	var got []domain.Event
	for e := range ch {
		got = append(got, e)
	}
	if len(got) != subscriberBuffer {
		t.Fatalf("subscriber received %d events before its channel closed, want %d", len(got), subscriberBuffer)
	}
	again, gap, cancel2 := l.Subscribe(got[len(got)-1].ID)
	defer cancel2()
	if gap {
		t.Fatal("Subscribe(last received) reported a gap, want the missed events replayed")
	}
	var replayed int
	for e := range again {
		replayed++
		if e.ID == last.ID {
			break
		}
	}
	if replayed != 10 {
		t.Errorf("replayed %d events, want the 10 the dropped subscriber missed", replayed)
	}
}
