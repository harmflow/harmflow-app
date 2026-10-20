package eventlog

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
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
