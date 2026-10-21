package eventlog

import (
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
