package eventlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
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
	// ordinary text, or inside another word, are left as they are.
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

// The agent's and the harness's text reaches the daemon as structs, so every string in one is redacted in place,
// however deep it sits; numbers, times, and unexported fields are left alone.
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

// An attempt's capture reads back redacted, its end from the start of a line when it is too long.
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

// One JSONL per execution, replay after an id, and live delivery with a backlog after an id.
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

// A replay after an id reads a running execution's log from the line after it, and returns what a read of the whole
// file does: from that log, from one that closed the file, from one opened afresh, as after a restart, which has no
// index, and from one that appends to a file that already holds lines.
func TestReplayAfterAnIDStartsAtItsLine(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { now = now.Add(time.Millisecond); return now }
	l, err := New(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	var ids, others []int64
	for i := range 40 {
		ids = append(ids, l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventToolStarted,
			Message: strings.Repeat("x", 100*i)}).ID)
		// Another execution's events take the ids between two of e1's.
		others = append(others, l.Append(domain.Event{ExecutionID: "e2", Type: domain.EventToolStarted}).ID)
	}
	path := filepath.Join(dir, "e1.jsonl")
	// whole is what a read of the whole file returns after an id, and where the line of each event starts.
	whole := func(after int64) ([]int64, map[int64]int64) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var want []int64
		starts := map[int64]int64{}
		offset := int64(0)
		for _, line := range strings.SplitAfter(string(data), "\n") {
			var e domain.Event
			if json.Unmarshal([]byte(line), &e) == nil {
				starts[e.ID] = offset
				if e.ID > after {
					want = append(want, e.ID)
				}
			}
			offset += int64(len(line))
		}
		return want, starts
	}
	check := func(t *testing.T, l *Log, after int64, limit int) {
		t.Helper()
		events, expired, err := l.Replay("e1", after, limit)
		var got []int64
		for _, e := range events {
			got = append(got, e.ID)
		}
		want, _ := whole(after)
		if limit > 0 && len(want) > limit {
			want = want[:limit]
		}
		if err != nil || expired || !slices.Equal(got, want) {
			t.Errorf("Replay(e1, %d, %d) = %v, %v, %v; want %v, false, nil", after, limit, got, expired, err, want)
		}
	}
	afters := []int64{0, ids[0], ids[19], others[19], ids[38], ids[39]}
	for _, after := range afters {
		check(t, l, after, 0)
	}
	check(t, l, ids[10], 5)

	// The index puts the start at the first line after the id, and at the end of the file after the last event.
	_, starts := whole(0)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for after, want := range map[int64]int64{0: 0, ids[0]: starts[ids[1]], ids[19]: starts[ids[20]],
		others[19]: starts[ids[20]], ids[38]: starts[ids[39]], ids[39]: info.Size()} {
		if got := l.start("e1", after); got != want {
			t.Errorf("start(e1, %d) = %d, want %d", after, got, want)
		}
	}
	// A replay from the end of the file returns what was appended since.
	last := l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventExecutionCompleted})
	if events, _, err := l.Replay("e1", ids[39], 0); err != nil || len(events) != 1 || events[0].ID != last.ID {
		t.Errorf("Replay(e1, %d, 0) = %+v, %v; want the event appended since, %d", ids[39], events, err, last.ID)
	}
	ids = append(ids, last.ID)

	// The execution ended: its file closed, and its index with it.
	l.Close("e1")
	if got := l.start("e1", ids[38]); got != 0 {
		t.Errorf("start(e1, %d) after Close = %d, want 0", ids[38], got)
	}
	for _, after := range afters {
		check(t, l, after, 0)
	}

	// After a restart, a log has no index until it appends, and then only for the lines it appends.
	fresh, err := New(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, after := range afters {
		check(t, fresh, after, 0)
	}
	late := []int64{fresh.Append(domain.Event{ExecutionID: "e1", Type: domain.EventToolStarted}).ID,
		fresh.Append(domain.Event{ExecutionID: "e1", Type: domain.EventToolStarted}).ID}
	_, starts = whole(0)
	if got := fresh.start("e1", late[0]); got != starts[late[1]] {
		t.Errorf("start(e1, %d) after a restart = %d, want %d", late[0], got, starts[late[1]])
	}
	if got := fresh.start("e1", ids[20]); got != 0 {
		t.Errorf("start(e1, %d), an id before the restart, = %d, want 0", ids[20], got)
	}
	for _, after := range append(afters, ids[40], late[0], late[1]) {
		check(t, fresh, after, 0)
	}
}

// A write that fails may leave part of a line, which runs into the next one, so the file loses its index: a replay
// reads it from the start, and returns the events it holds. Once the execution's file opens again, it has an index.
func TestFailedWriteDropsTheIndex(t *testing.T) {
	l, err := New(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	first := l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventExecutionStarted})
	second := l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventToolStarted})
	if got := l.start("e1", first.ID); got == 0 {
		t.Fatalf("start(e1, %d) = 0, want the offset of the second line", first.ID)
	}
	_ = l.files["e1"].f.Close() // every write to it fails from now on
	l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventToolCompleted})
	if got := l.start("e1", first.ID); got != 0 {
		t.Errorf("start(e1, %d) after a failed write = %d, want 0", first.ID, got)
	}
	if events, _, err := l.Replay("e1", first.ID, 0); err != nil || len(events) != 1 || events[0].ID != second.ID {
		t.Errorf("Replay(e1, %d, 0) = %+v, %v; want the event the file holds after it, %d", first.ID, events, err, second.ID)
	}
	l.Close("e1")
	third := l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventToolStarted})
	fourth := l.Append(domain.Event{ExecutionID: "e1", Type: domain.EventExecutionCompleted})
	if events, _, err := l.Replay("e1", third.ID, 0); err != nil || len(events) != 1 || events[0].ID != fourth.ID ||
		l.start("e1", third.ID) == 0 {
		t.Errorf("Replay(e1, %d, 0) once the file opened again = %+v, %v, from %d; want %d from its line", third.ID, events,
			err, l.start("e1", third.ID), fourth.ID)
	}
}

// Replay reads a log with no lock held, so Append, which takes the log's lock for every event of every execution, never
// waits for a replay that reads. A FIFO stands in for the log: the replay waits in its read until the test writes.
func TestAppendDoesNotWaitForAReplay(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "e1.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("Mkfifo(%s): %v", fifo, err)
	}
	type replay struct {
		events []domain.Event
		err    error
	}
	replayed := make(chan replay, 1) // the replay never waits to hand its result over, even to a test that failed
	go func() {
		events, _, err := l.Replay("e1", 0, 0)
		replayed <- replay{events, err}
	}()
	// Opening a FIFO to write without waiting succeeds once a reader has it open: the replay is then reading.
	var w *os.File
	for deadline := time.Now().Add(10 * time.Second); w == nil; time.Sleep(time.Millisecond) {
		if w, err = os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err != nil && time.Now().After(deadline) {
			t.Fatalf("the replay did not open the log: %v", err)
		}
	}
	defer w.Close()
	appended := make(chan domain.Event, 1) // as replayed
	go func() { appended <- l.Append(domain.Event{ExecutionID: "e2", Type: domain.EventExecutionStarted}) }()
	select {
	case <-appended:
	case <-time.After(10 * time.Second):
		t.Fatal("Append waited for a replay that reads")
	}
	line, err := json.Marshal(domain.Event{ID: 7, ExecutionID: "e1", Type: domain.EventExecutionStarted})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-replayed:
		if r.err != nil || len(r.events) != 1 || r.events[0].ID != 7 {
			t.Errorf("Replay(e1, 0, 0) = %+v, %v; want the event written, 7", r.events, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Replay did not return once the log was written and closed")
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
