package app

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// conversingHarness is the fake harness of its fakeHarness's ID, spoken to while it runs as Codex's app server is: its
// script reads the answers from its stdin.
type conversingHarness struct {
	*fakeHarness
}

var _ ports.HarnessAdapter = (*conversingHarness)(nil)

func (c *conversingHarness) Prepare(info ports.HarnessInfo, req ports.RunRequest) (ports.Invocation, ports.OutputParser, error) {
	inv, out, err := c.fakeHarness.Prepare(info, req)
	if err != nil {
		return inv, out, err
	}
	inv.Interactive = true
	return inv, &conversingParser{fakeParser: out.(*fakeParser)}, nil
}

// conversingParser reads `ASK <id> <command>` as an approval request and writes each answer back as a line of its
// own, `<id> <decision>`.
type conversingParser struct {
	*fakeParser
	mu      sync.Mutex
	in      io.WriteCloser
	pending map[string]domain.ApprovalRequest
}

var _ ports.Conversation = (*conversingParser)(nil)

func (p *conversingParser) Begin(stdin io.WriteCloser) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.in = stdin
	return nil
}

func (p *conversingParser) Line(b []byte) []ports.HarnessEvent {
	if rest, ok := strings.CutPrefix(string(b), "ASK "); ok {
		id, command, _ := strings.Cut(rest, " ")
		q := domain.ApprovalRequest{ID: id, Kind: domain.ApprovalCommand, Command: command, Cwd: "/w"}
		p.mu.Lock()
		if p.pending == nil {
			p.pending = map[string]domain.ApprovalRequest{}
		}
		p.pending[id] = q
		p.mu.Unlock()
		return []ports.HarnessEvent{{Kind: ports.HarnessApprovalRequested, Approval: &q}}
	}
	return p.fakeParser.Line(b)
}

func (p *conversingParser) Answer(id string, d domain.ApprovalDecision) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.pending[id]; !ok {
		return domain.TextErr("approval_not_pending", "no approval request {{id}} waits for an answer", "id", id)
	}
	delete(p.pending, id)
	_, err := io.WriteString(p.in, id+" "+string(d)+"\n")
	return err
}

func (p *conversingParser) Pending() []domain.ApprovalRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []domain.ApprovalRequest
	for _, q := range p.pending {
		out = append(out, q)
	}
	return out
}

// conversing starts an env whose Claude Code stand-in is spoken to while it runs.
func conversing(t *testing.T, configure ...func(*env)) *env {
	t.Helper()
	return newEnv(t, append([]func(*env){func(e *env) { e.signer = &conversingHarness{fakeHarness: e.claude} }},
		configure...)...)
}

// harmflow-app#465: an approval request waits for the owner. The execution says it waits, the owner is notified with
// the command, though the task chose no notice of its own, and HarmFlow sends exactly the decision the owner gave; the
// events record it, with who gave it and when. A request is answered once.
func TestApprovalWaitsForTheOwnersAnswer(t *testing.T) {
	e := conversing(t)
	e.claude.setScript(func(ports.RunRequest) string {
		// The attempt succeeds only when the harness read exactly the owner's decision.
		return `echo 'ASK 7 curl https://example.com'; read -r answer; ` +
			`if [ "$answer" = "7 accept_for_session" ]; then echo 'END ok'; else echo 'END wrong'; fi`
	})
	e.create("asks", "  notifications:\n    native:\n      events: []\n")
	r, err := e.app.RunTask("asks")
	if err != nil {
		t.Fatal(err)
	}
	if !e.poll(10*time.Second, func() bool { return len(e.eventsOf(r.ExecutionID, domain.EventApprovalRequested)) == 1 }) {
		t.Fatalf("approval.requested events of %s = %d after 10s, want 1", r.ExecutionID,
			len(e.eventsOf(r.ExecutionID, domain.EventApprovalRequested)))
	}
	asked := e.eventsOf(r.ExecutionID, domain.EventApprovalRequested)[0]
	if asked.Payload["requestId"] != "7" || asked.Payload["command"] != "curl https://example.com" ||
		asked.Payload["cwd"] != "/w" {
		t.Errorf("approval.requested payload %v, want the request in full", asked.Payload)
	}
	x, _ := e.app.GetExecution(r.ExecutionID)
	if x.Status != domain.StatusRunning || x.CurrentStepKey != "step_waiting_approval" {
		t.Errorf("execution %s, step %q (%s); want running and waiting for the owner", x.Status, x.CurrentStepKey,
			x.CurrentStep)
	}
	var notice *Notification
	for _, n := range e.app.Notifications(false) {
		if n.EventType == domain.EventApprovalRequested && n.ExecutionID == r.ExecutionID {
			notice = &n
		}
	}
	if notice == nil || notice.Params["command"] != "curl https://example.com" {
		t.Errorf("notice %+v, want one for the request, naming its command", notice)
	}

	if err := e.app.AnswerApproval(r.ExecutionID, "7", "cancel"); err == nil {
		t.Error("AnswerApproval(cancel) = nil, want it refused: HarmFlow does not offer cancel")
	}
	if err := e.app.AnswerApproval(r.ExecutionID, "8", domain.ApprovalAccept); err == nil {
		t.Error("AnswerApproval(8) = nil, want it refused: no request 8 waits")
	}
	if err := e.app.AnswerApproval(r.ExecutionID, "7", domain.ApprovalAcceptForSession); err != nil {
		t.Fatalf("AnswerApproval(7) = %v", err)
	}
	e.wait(r.ExecutionID, domain.StatusCompleted, domain.StatusNoWork)
	answered := e.eventsOf(r.ExecutionID, domain.EventApprovalAnswered)
	if len(answered) != 1 || answered[0].Payload["decision"] != "accept_for_session" || answered[0].Payload["by"] != "owner" ||
		answered[0].Payload["answeredAt"] == nil {
		t.Errorf("approval.answered %+v, want the owner's accept_for_session, with its time", answered)
	}
	if err := e.app.AnswerApproval(r.ExecutionID, "7", domain.ApprovalDecline); err == nil {
		t.Errorf("second AnswerApproval(%s, 7, decline) = nil, want an error", r.ExecutionID)
	}
}

// harmflow-app#465: no timeout accepts a request. One still unanswered when the execution reaches its time limit is
// declined, and the execution says so.
func TestApprovalDeclinedAtTheTimeLimit(t *testing.T) {
	e := conversing(t, func(e *env) { e.timeoutUnit = time.Second })
	e.claude.setScript(func(ports.RunRequest) string {
		return `echo 'ASK 3 rm -rf /tmp/x'; read -r answer; echo "GOT $answer" >&2; sleep 30`
	})
	e.create("slow", "  execution:\n    timeoutMinutes: 2\n    cancelGraceSeconds: 1\n")
	r, err := e.app.RunTask("slow")
	if err != nil {
		t.Fatal(err)
	}
	x := e.waitWithin(30*time.Second, r.ExecutionID, domain.StatusFailed)
	if x.ErrorKey != "execution_timed_out" {
		t.Errorf("the execution failed with %q (%s), want execution_timed_out", x.ErrorKey, x.ErrorMessage)
	}
	answered := e.eventsOf(r.ExecutionID, domain.EventApprovalAnswered)
	if len(answered) != 1 || answered[0].Payload["decision"] != "decline" || answered[0].Payload["by"] != "time_limit" ||
		answered[0].MessageKey != "approval_declined_time_limit" {
		t.Errorf("approval.answered %+v, want request 3 declined by the time limit, saying so", answered)
	}
}
