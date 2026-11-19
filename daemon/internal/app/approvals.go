package app

import (
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// A harness spoken to while it runs can ask the owner to approve a command outside its sandbox, a change of files, or
// more permissions. The request waits for the owner: HarmFlow records it, shows that the execution waits, notifies the
// owner, and sends back exactly the owner's answer. No rule, setting, or timeout accepts one; a request still
// unanswered at the execution's time limit is declined, and the execution says so.

// approvalBy says who gave an answer: the owner, or the time limit that declined it.
const (
	approvalByOwner     = "owner"
	approvalByTimeLimit = "time_limit"
)

// harnessLabel is the running attempt's harness, as the texts name it. The executor sets it before the attempt's
// process starts, so no request of the attempt comes before it.
func (r *run) harnessLabel() string {
	r.a.mu.Lock()
	defer r.a.mu.Unlock()
	return r.harnessName
}

// onApprovalRequested records a request that waits for the owner, says on the execution that it waits, and notifies
// the owner.
func (r *run) onApprovalRequested(q domain.ApprovalRequest) {
	what := q.Command
	if what == "" {
		what = string(q.Kind)
	}
	r.event(domain.Event{Type: domain.EventApprovalRequested, Severity: domain.SeverityWarning,
		Payload: q.Payload()}.Says(domain.T("approval_requested", "{{harness}} asks for approval: {{what}}",
		"harness", r.harnessLabel(), "what", trimTo(what, 300))))
	r.setStatus(domain.StatusRunning, domain.T("step_waiting_approval", "Waiting for the owner's approval"))
}

// afterApproval gives the step back to the harness once no request waits.
func (r *run) afterApproval() {
	r.a.mu.Lock()
	conv := r.conv
	r.a.mu.Unlock()
	if conv != nil && len(conv.Pending()) > 0 {
		return
	}
	r.setStatus(domain.StatusRunning, domain.T("step_harness_running", "{{harness}} is running", "harness",
		r.harnessLabel()))
}

// onAutoReview records the harness's own automatic review of an approval, which HarmFlow never answers for.
func (r *run) onAutoReview(kind ports.HarnessEventKind, v domain.AutoReview) {
	payload := map[string]any{"reviewId": v.ID}
	for k, s := range map[string]string{"action": v.Action, "status": v.Status, "risk": v.Risk, "reason": v.Reason,
		"command": v.Command} {
		if s != "" {
			payload[k] = s
		}
	}
	if kind == ports.HarnessAutoReviewStarted {
		r.event(domain.Event{Type: domain.EventAutoReviewStarted, Payload: payload}.Says(
			domain.T("auto_review_started", "{{harness}}'s automatic review of an approval started", "harness",
				r.harnessLabel())))
		return
	}
	r.event(domain.Event{Type: domain.EventAutoReviewCompleted, Payload: payload}.Says(
		domain.T("auto_review_completed", "{{harness}}'s automatic review of an approval ended: {{status}}",
			"harness", r.harnessLabel(), "status", v.Status)))
}

// answerApproval sends one decision and records it, with who gave it and when.
func (r *run) answerApproval(conv ports.Conversation, q domain.ApprovalRequest, d domain.ApprovalDecision, by string) error {
	if err := conv.Answer(q.ID, d); err != nil {
		return err
	}
	payload := q.Payload()
	payload["decision"], payload["by"], payload["answeredAt"] = string(d), by, r.a.now()
	text := domain.T("approval_answered", "The owner answered {{decision}}", "decision", string(d))
	if by == approvalByTimeLimit {
		text = domain.T("approval_declined_time_limit",
			"Declined: the request was still unanswered when the execution reached its time limit")
	}
	r.event(domain.Event{Type: domain.EventApprovalAnswered, Payload: payload}.Says(text))
	r.afterApproval()
	return nil
}

// declinePending declines every request that still waits, as the time limit does.
func (r *run) declinePending(conv ports.Conversation) {
	for _, q := range conv.Pending() {
		if err := r.answerApproval(conv, q, domain.ApprovalDecline, approvalByTimeLimit); err != nil {
			r.a.log.Printf("execution %s: declining approval %s at the time limit: %v", r.x.ID, q.ID, err)
		}
	}
}

// AnswerApproval sends the owner's decision for one request of a running execution.
func (a *App) AnswerApproval(executionID, requestID string, d domain.ApprovalDecision) error {
	if !d.Valid() {
		return newError(CodeBadRequest, domain.T("approval_decision_invalid", "{{decision}} is not a decision HarmFlow offers",
			"decision", string(d)))
	}
	a.mu.Lock()
	x, ok := a.executions[executionID]
	if !ok {
		_, ended := a.ended[executionID]
		a.mu.Unlock()
		if !ended {
			return executionNotFound(executionID)
		}
		return newError(CodeConflict, domain.T("approval_not_pending", "no approval request {{id}} waits for an answer",
			"id", requestID))
	}
	var r *run
	var conv ports.Conversation
	if h := a.running[x.TaskID]; h != nil && h.run != nil && h.run.x == x {
		r, conv = h.run, h.run.conv
	}
	a.mu.Unlock()
	if conv == nil {
		return newError(CodeConflict, domain.T("approval_not_pending", "no approval request {{id}} waits for an answer",
			"id", requestID))
	}
	for _, q := range conv.Pending() {
		if q.ID == requestID {
			if err := r.answerApproval(conv, q, d, approvalByOwner); err != nil {
				return newError(CodeConflict, domain.TextOf(err))
			}
			return nil
		}
	}
	return newError(CodeConflict, domain.T("approval_not_pending", "no approval request {{id}} waits for an answer",
		"id", requestID))
}

// notifyApproval notifies the owner of a request that waits for them, whichever events the task chose: only the owner
// answers it. The global level that turns every notice off still does.
func (a *App) notifyApproval(e domain.Event) {
	a.mu.Lock()
	level := a.settings.NotificationLevel
	t := a.tasks[e.TaskID]
	var name string
	if t != nil {
		name = t.doc.Metadata.Name
	}
	a.mu.Unlock()
	if level == "none" || t == nil {
		return
	}
	n := Notification{ID: newNoticeID(), CreatedAt: a.now(), EventType: e.Type, TaskID: e.TaskID,
		ExecutionID: e.ExecutionID, Severity: "warning", Params: map[string]string{"taskName": name}}
	if e.ExecutionID != "" {
		n.Params["executionId"] = e.ExecutionID
	}
	if e.Harness != "" {
		n.Params["harness"] = e.Harness
	}
	what, _ := e.Payload["command"].(string)
	if what == "" {
		what, _ = e.Payload["kind"].(string)
	}
	n.Params["command"] = trimTo(what, 300)
	n.Title, n.Body = englishNotice(e.Type, name, n.Params)
	a.keepNotice(n)
}
