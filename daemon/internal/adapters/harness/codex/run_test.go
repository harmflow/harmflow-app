package codex

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// started is a parser whose thread and turn started, as Codex's app server answers them.
func started(t *testing.T) (*parser, *stdinRecorder) {
	t.Helper()
	p := &parser{thread: map[string]any{"cwd": "/w"}, turn: map[string]any{"input": []map[string]any{{"type": "text", "text": "do it"}}}}
	in := begun(t, p)
	feed(p, `{"id":1,"result":{}}`, `{"id":2,"result":{"thread":{"id":"th-1"},"approvalPolicy":"on-request"}}`,
		`{"id":3,"result":{"turn":{"id":"turn-1"}}}`)
	return p, in
}

// harmflow-app#465: the attempt says hello, starts the thread with the task's settings, then the turn with the
// instructions, each once the server answered the request before it.
func TestConversationOpensTheSession(t *testing.T) {
	p := &parser{thread: map[string]any{"cwd": "/w", "approvalPolicy": "never"},
		turn: map[string]any{"input": []map[string]any{{"type": "text", "text": "do it"}}}}
	in := begun(t, p)
	if m := in.sent(t, "initialize"); m["id"] != float64(1) {
		t.Errorf("initialize = %v, want id 1", m)
	}
	feed(p, `{"id":1,"result":{}}`)
	in.sent(t, "initialized")
	if params, _ := in.sent(t, "thread/start")["params"].(map[string]any); params["cwd"] != "/w" {
		t.Errorf("thread/start params %v, want the task's settings", params)
	}
	events := feed(p, `{"id":2,"result":{"thread":{"id":"th-1"},"model":"gpt-x","reasoningEffort":"high","approvalPolicy":"never"}}`)
	if len(events) != 1 || events[0].Kind != ports.HarnessStarted || events[0].SessionID != "th-1" ||
		events[0].Model != "gpt-x" || events[0].Effort != "high" {
		t.Errorf("thread start = %+v, want the session, its model and effort", events)
	}
	params, _ := in.sent(t, "turn/start")["params"].(map[string]any)
	if params["threadId"] != "th-1" || params["input"] == nil {
		t.Errorf("turn/start params %v, want the thread and the instructions", params)
	}
	// The turn's end closes the server's stdin, which ends it.
	feed(p, `{"method":"turn/completed","params":{"turn":{"status":"completed"}}}`)
	if !in.closed {
		t.Error("the turn ended and the server's stdin is still open")
	}
	if end := p.Finish(0, "", false, false); end.Class != domain.ClassNone || end.SessionID != "th-1" {
		t.Errorf("Finish = %+v, want a success of session th-1", end)
	}
}

// A request that fails to start the thread ends the attempt with the server's own error.
func TestConversationThatCannotStart(t *testing.T) {
	p := &parser{}
	in := begun(t, p)
	feed(p, `{"id":1,"error":{"code":-1,"message":"Not logged in"}}`)
	if !in.closed {
		t.Error("the attempt could not start and the server's stdin is still open")
	}
	if end := p.Finish(1, "", false, false); end.Class != domain.ClassHarnessUnavailable || !end.SignedOut {
		t.Errorf("Finish = %+v, want a signed-out harness", end)
	}
}

// harmflow-app#465: an approval request waits for the owner, named in full; HarmFlow sends exactly the decision the
// owner gave, in Codex's words, and the request waits no more.
func TestApprovalRequestWaitsForTheOwner(t *testing.T) {
	for _, c := range []struct {
		decision domain.ApprovalDecision
		want     string
	}{
		{domain.ApprovalAccept, "accept"},
		{domain.ApprovalAcceptForSession, "acceptForSession"},
		{domain.ApprovalDecline, "decline"},
	} {
		t.Run(string(c.decision), func(t *testing.T) {
			p, in := started(t)
			events := feed(p, `{"id":7,"method":"item/commandExecution/requestApproval","params":{"itemId":"i1",`+
				`"threadId":"th-1","turnId":"turn-1","startedAtMs":1,"command":"/bin/zsh -lc 'curl https://example.com'",`+
				`"cwd":"/w","reason":"needs the network","networkApprovalContext":{"host":"example.com","protocol":"https"}}}`)
			want := domain.ApprovalRequest{ID: "7", Kind: domain.ApprovalCommand, Command: "curl https://example.com",
				Cwd: "/w", Reason: "needs the network", NetworkHost: "example.com"}
			if len(events) != 1 || events[0].Kind != ports.HarnessApprovalRequested || events[0].Approval == nil ||
				!approvalEqual(*events[0].Approval, want) {
				t.Fatalf("request = %+v, want one approval requested %+v", events, want)
			}
			if got := p.Pending(); len(got) != 1 || got[0].ID != "7" {
				t.Fatalf("Pending() = %+v, want request 7", got)
			}
			if err := p.Answer("7", c.decision); err != nil {
				t.Fatal(err)
			}
			answer := in.sent(t, "7")
			if result, _ := answer["result"].(map[string]any); result["decision"] != c.want {
				t.Errorf("answer = %v, want the decision %q", answer, c.want)
			}
			if len(p.Pending()) != 0 {
				t.Errorf("Pending() = %+v after the answer, want none", p.Pending())
			}
			if err := p.Answer("7", c.decision); err == nil {
				t.Error("a second answer to request 7 was sent")
			}
		})
	}
}

func approvalEqual(a, b domain.ApprovalRequest) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.Command == b.Command && a.Cwd == b.Cwd && a.Reason == b.Reason &&
		a.NetworkHost == b.NetworkHost && slices.Equal(a.Paths, b.Paths) && a.Network == b.Network
}

// A file change names the files of the change Codex started, and more permissions are granted back as Codex asked,
// for the turn or for the session, or not at all.
func TestApprovalOfFilesAndPermissions(t *testing.T) {
	p, in := started(t)
	feed(p, `{"method":"item/started","params":{"item":{"id":"fc","type":"fileChange","status":"inProgress",`+
		`"changes":[{"path":"/etc/hosts","kind":{"type":"update"},"diff":""}]}}}`)
	events := feed(p, `{"id":"r-1","method":"item/fileChange/requestApproval","params":{"itemId":"fc","threadId":"th-1",`+
		`"turnId":"turn-1","startedAtMs":1,"reason":"outside the workspace"}}`)
	if len(events) != 1 || events[0].Approval.Kind != domain.ApprovalFileChange ||
		!slices.Equal(events[0].Approval.Paths, []string{"/etc/hosts"}) || events[0].Approval.ID != "r-1" {
		t.Errorf("file change request = %+v, want /etc/hosts", events)
	}
	if err := p.Answer("r-1", domain.ApprovalDecline); err != nil {
		t.Fatal(err)
	}
	if r, _ := in.sent(t, "r-1")["result"].(map[string]any); r["decision"] != "decline" {
		t.Errorf("file change answer = %v, want decline", r)
	}

	asked := `{"fileSystem":{"write":["/out"]},"network":{"enabled":true}}`
	for _, c := range []struct {
		decision  domain.ApprovalDecision
		wantScope string
		wantGrant bool
	}{
		{domain.ApprovalAccept, "turn", true},
		{domain.ApprovalAcceptForSession, "session", true},
		{domain.ApprovalDecline, "", false},
	} {
		events := feed(p, `{"id":9,"method":"item/permissions/requestApproval","params":{"itemId":"x","threadId":"th-1",`+
			`"turnId":"turn-1","startedAtMs":1,"cwd":"/w","permissions":`+asked+`}}`)
		if len(events) != 1 || !events[0].Approval.Network || !slices.Equal(events[0].Approval.Paths, []string{"/out"}) {
			t.Fatalf("permissions request = %+v, want /out and the network", events)
		}
		if err := p.Answer("9", c.decision); err != nil {
			t.Fatal(err)
		}
		result, _ := in.sent(t, "9")["result"].(map[string]any)
		granted, _ := json.Marshal(result["permissions"])
		if c.wantGrant && (result["scope"] != c.wantScope || string(granted) != asked) {
			t.Errorf("%s: answer %v, want %s granted for the %s", c.decision, result, asked, c.wantScope)
		}
		if !c.wantGrant && string(granted) != "{}" {
			t.Errorf("%s: answer %v, want nothing granted", c.decision, result)
		}
	}
}

// harmflow-app#465: HarmFlow never answers a request by itself. A request Codex resolved on its own, or one whose turn
// ended, waits no more and is withdrawn; a request HarmFlow has no answer for is refused, and is no approval.
func TestApprovalWithdrawnAndOtherRequests(t *testing.T) {
	p, in := started(t)
	feed(p, `{"id":4,"method":"item/commandExecution/requestApproval","params":{"itemId":"a","command":"ls"}}`,
		`{"id":5,"method":"item/commandExecution/requestApproval","params":{"itemId":"b","command":"pwd"}}`)
	events := feed(p, `{"method":"serverRequest/resolved","params":{"threadId":"th-1","requestId":4}}`)
	if len(events) != 1 || events[0].Kind != ports.HarnessApprovalWithdrawn || events[0].Approval.ID != "4" {
		t.Errorf("resolved = %+v, want request 4 withdrawn", events)
	}
	events = feed(p, `{"method":"turn/completed","params":{"turn":{"status":"interrupted"}}}`)
	if len(events) != 1 || events[0].Kind != ports.HarnessApprovalWithdrawn || events[0].Approval.ID != "5" ||
		len(p.Pending()) != 0 {
		t.Errorf("turn end = %+v, pending %+v; want request 5 withdrawn and none pending", events, p.Pending())
	}
	if events := feed(p, `{"id":6,"method":"item/tool/requestUserInput","params":{}}`); len(events) != 0 {
		t.Errorf("a request for input = %+v, want no event", events)
	}
	if e, _ := in.sent(t, "6")["error"].(map[string]any); e == nil {
		t.Error("a request HarmFlow cannot answer got no error back")
	}
	if err := p.Answer("4", "cancel"); err == nil {
		t.Error("cancel was accepted as a decision")
	}
}

// harmflow-app#465: Codex's automatic review of an approval is recorded, its start and its outcome, and HarmFlow
// answers nothing for it.
func TestAutoReviewIsRecorded(t *testing.T) {
	p, in := started(t)
	before := len(in.lines)
	events := feed(p, `{"method":"item/autoApprovalReview/started","params":{"reviewId":"rv","threadId":"th-1",`+
		`"turnId":"turn-1","startedAtMs":1,"action":{"type":"command","command":"rm -rf build","cwd":"/w","source":"shell"},`+
		`"review":{"status":"inProgress"}}}`,
		`{"method":"item/autoApprovalReview/completed","params":{"reviewId":"rv","threadId":"th-1","turnId":"turn-1",`+
			`"startedAtMs":1,"completedAtMs":2,"decisionSource":"agent","action":{"type":"command","command":"rm -rf build",`+
			`"cwd":"/w","source":"shell"},"review":{"status":"denied","riskLevel":"high","rationale":"deletes files"}}}`)
	if len(events) != 2 || events[0].Kind != ports.HarnessAutoReviewStarted || events[1].Kind != ports.HarnessAutoReviewCompleted ||
		events[1].Review.Status != "denied" || events[1].Review.Risk != "high" || events[1].Review.Command != "rm -rf build" {
		t.Errorf("auto review = %+v, want its start and its denial", events)
	}
	if len(in.lines) != before {
		t.Errorf("the parser wrote %q for an automatic review, want nothing", in.lines[before:])
	}
}
