package app

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// Notification is a notice for the user. The daemon only records and emits it; the tray process of the desktop shows
// it natively, and one nobody could show is held as unread until the dashboard opens (D-074).
type Notification struct {
	ID          string            `json:"id"`
	CreatedAt   time.Time         `json:"createdAt"`
	EventType   string            `json:"eventType"`
	TaskID      string            `json:"taskId,omitempty"`
	ExecutionID string            `json:"executionId,omitempty"`
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	Severity    string            `json:"severity"`
	Read        bool              `json:"read"`
	Params      map[string]string `json:"params"`
}

// maxNotifications is the size read notices are trimmed to; unread ones are never trimmed (FS-011 FR-005).
const maxNotifications = 500

// failureEvents pass the "failures and attention only" level.
var failureEvents = map[string]bool{
	domain.EventExecutionFailed: true, domain.EventExecutionNeedsAttention: true, domain.EventTestFailed: true,
	domain.EventHarnessUnavailable: true, domain.EventHarnessNoAlternative: true,
}

// reasonParam names the param a notice's text reads the event's message from: why it failed, or why it was sent.
var reasonParam = map[string]string{
	domain.EventExecutionFailed: "error", domain.EventExecutionNeedsAttention: "reason",
	domain.EventHarnessUnavailable: "reason", domain.EventHarnessNoAlternative: "reason",
}

// notifiable maps an emitted event to the notifiable event a task chooses (FS-011 FR-002).
func notifiable(e domain.Event) string {
	switch e.Type {
	case domain.EventExecutionStarted, domain.EventExecutionCompleted, domain.EventExecutionFailed,
		domain.EventExecutionCancelled, domain.EventExecutionNeedsAttention, domain.EventPullRequestCreated,
		domain.EventPullRequestUpdated, domain.EventTestFailed, domain.EventHarnessUnavailable,
		domain.EventHarnessNoAlternative:
		return e.Type
	case domain.EventExecutionInterrupted:
		return domain.EventExecutionFailed
	}
	return ""
}

// notifyFor creates a notice when the event is one the task chose and the global level lets it through (D-074). An
// event of a running execution comes with the definition it started with, so an edit, a new name included, applies from
// the next execution (D-083); any other event reads the current one. The global level is a setting, not part of the
// task, and applies at once.
func (a *App) notifyFor(e domain.Event, started *domain.TaskDocument) {
	kind := notifiable(e)
	if kind == "" || e.TaskID == "" {
		return
	}
	a.mu.Lock()
	level := a.settings.NotificationLevel
	t := a.tasks[e.TaskID]
	var doc domain.TaskDocument
	if started != nil {
		doc = *started
	} else if t != nil {
		doc = t.doc
	}
	a.mu.Unlock()
	switch level {
	case "none":
		return
	case "failures_and_attention":
		if !failureEvents[kind] {
			return
		}
	}
	if t == nil {
		return
	}
	native := doc.Effective().Spec.Notifications.Native
	if !*native.Enabled || !containsString(native.Events, kind) {
		return
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	n := Notification{ID: "ntf-" + hex.EncodeToString(b[:]), CreatedAt: a.now(), EventType: kind, TaskID: e.TaskID,
		ExecutionID: e.ExecutionID, Severity: "info", Params: map[string]string{"taskName": doc.Metadata.Name}}
	if e.ExecutionID != "" {
		n.Params["executionId"] = e.ExecutionID
	}
	if url, ok := e.Payload["url"].(string); ok && url != "" {
		n.Params["url"] = url
	}
	if url, ok := e.Payload["pullRequestUrl"].(string); ok && url != "" {
		n.Params["url"] = url
	}
	if e.Harness != "" {
		n.Params["harness"] = e.Harness
	}
	if name := reasonParam[kind]; name != "" && e.Message != "" {
		// A message the daemon wrote is nested with its key, so the desktop translates it in place; the agent's words
		// have no key and are quoted as they are. Its params are trimmed like it, so a translated notice stays short.
		said := domain.Text{Message: trimTo(e.Message, 300), Key: e.MessageKey, Params: map[string]string{}}
		for k, v := range e.Params {
			said.Params[k] = trimTo(v, 300)
		}
		n.Params = domain.Text{Params: n.Params}.Nest(name, said).Params
	}
	if summary, _ := e.Payload["summary"].(string); kind == domain.EventExecutionCompleted && summary != "" {
		n.Params["summary"] = trimTo(summary, 300) // the agent's own summary
	}
	n.Title, n.Body = englishNotice(kind, doc.Metadata.Name, n.Params)
	if failureEvents[kind] {
		n.Severity = "error"
		if kind == domain.EventExecutionNeedsAttention || kind == domain.EventTestFailed {
			n.Severity = "warning"
		}
	}
	a.mu.Lock()
	a.notifications = trimNotifications(append([]Notification{n}, a.notifications...))
	a.mu.Unlock()
	a.saveNotifications()
	payload, _ := toMap(n).(map[string]any)
	a.d.Events.Append(domain.Event{Type: domain.EventNotificationCreated, TaskID: e.TaskID, ExecutionID: e.ExecutionID,
		Timestamp: n.CreatedAt, Message: n.Title, Payload: payload})
}

// trimNotifications drops the oldest read notices of a newest-first list until it is back within maxNotifications. It
// stops when no read notice is left, so a list of unread notices stays above the cap and none is lost (FS-011 FR-005).
func trimNotifications(list []Notification) []Notification {
	excess := len(list) - maxNotifications
	if excess <= 0 {
		return list
	}
	drop := map[int]bool{}
	for i := len(list) - 1; i >= 0 && len(drop) < excess; i-- {
		if list[i].Read {
			drop[i] = true
		}
	}
	out := make([]Notification, 0, len(list)-len(drop))
	for i, n := range list {
		if !drop[i] {
			out = append(out, n)
		}
	}
	return out
}

// englishNotice is the en-US fallback; the desktop translates from eventType and params (FS-012 FR-005).
func englishNotice(kind, task string, p map[string]string) (string, string) {
	switch kind {
	case domain.EventExecutionStarted:
		return "Execution started", task + " started."
	case domain.EventExecutionCompleted:
		return "Execution completed", task + " completed."
	case domain.EventExecutionFailed:
		return "Execution failed", task + " failed: " + p["error"]
	case domain.EventExecutionCancelled:
		return "Execution cancelled", task + " was cancelled."
	case domain.EventExecutionNeedsAttention:
		return "Needs your attention", task + " needs a human to act: " + p["reason"]
	case domain.EventPullRequestCreated:
		return "Draft pull request created", task + " opened " + p["url"]
	case domain.EventPullRequestUpdated:
		return "Draft pull request updated", task + " updated " + p["url"]
	case domain.EventTestFailed:
		return "Tests failed", task + ": tests failed and need review."
	case domain.EventHarnessUnavailable:
		return "Harness unavailable", task + ": " + p["reason"]
	case domain.EventHarnessNoAlternative:
		return "No harness available", task + ": " + p["reason"]
	}
	return kind, task
}

func (a *App) loadNotifications() {
	var list []Notification
	if ok, _ := a.d.State.Load("notifications", &list); ok {
		a.mu.Lock()
		a.notifications = list
		a.mu.Unlock()
	}
}

func (a *App) saveNotifications() {
	a.mu.Lock()
	list := append([]Notification(nil), a.notifications...)
	a.mu.Unlock()
	if err := a.d.State.Save("notifications", list); err != nil {
		a.log.Printf("saving notifications: %v", err)
	}
}

func (a *App) Notifications(unreadOnly bool) []Notification {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []Notification{}
	for _, n := range a.notifications {
		if unreadOnly && n.Read {
			continue
		}
		out = append(out, n)
	}
	return out
}

// MarkNotificationsRead marks the given notices read, or every notice when ids is nil.
func (a *App) MarkNotificationsRead(ids []string) {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	a.mu.Lock()
	for i := range a.notifications {
		if ids == nil || set[a.notifications[i].ID] {
			a.notifications[i].Read = true
		}
	}
	a.mu.Unlock()
	a.saveNotifications()
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
