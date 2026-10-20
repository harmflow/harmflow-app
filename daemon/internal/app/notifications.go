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

const maxNotifications = 500

// failureEvents pass the "failures and attention only" level.
var failureEvents = map[string]bool{
	domain.EventExecutionFailed: true, domain.EventExecutionNeedsAttention: true, domain.EventTestFailed: true,
	domain.EventHarnessUnavailable: true, domain.EventHarnessNoAlternative: true,
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

// notifyFor creates a notice when the event is one the task chose and the global level lets it through (D-074).
func (a *App) notifyFor(e domain.Event) {
	kind := notifiable(e)
	if kind == "" || e.TaskID == "" {
		return
	}
	a.mu.Lock()
	level := a.settings.NotificationLevel
	t := a.tasks[e.TaskID]
	var doc domain.TaskDocument
	if t != nil {
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
	if e.Message != "" {
		n.Params["detail"] = trimTo(e.Message, 300)
	}
	if e.Harness != "" {
		n.Params["harness"] = e.Harness
	}
	n.Title, n.Body = englishNotice(kind, doc.Metadata.Name, n.Params)
	if failureEvents[kind] {
		n.Severity = "error"
		if kind == domain.EventExecutionNeedsAttention || kind == domain.EventTestFailed {
			n.Severity = "warning"
		}
	}
	a.mu.Lock()
	a.notifications = append([]Notification{n}, a.notifications...)
	if len(a.notifications) > maxNotifications {
		a.notifications = a.notifications[:maxNotifications]
	}
	a.mu.Unlock()
	a.saveNotifications()
	payload, _ := toMap(n).(map[string]any)
	a.d.Events.Append(domain.Event{Type: domain.EventNotificationCreated, TaskID: e.TaskID, ExecutionID: e.ExecutionID,
		Timestamp: n.CreatedAt, Message: n.Title, Payload: payload})
}

// englishNotice is the en-US fallback; the desktop translates from eventType and params (FS-012 FR-005).
func englishNotice(kind, task string, p map[string]string) (string, string) {
	switch kind {
	case domain.EventExecutionStarted:
		return "Execution started", task + " started."
	case domain.EventExecutionCompleted:
		return "Execution completed", task + " completed."
	case domain.EventExecutionFailed:
		return "Execution failed", task + " failed: " + p["detail"]
	case domain.EventExecutionCancelled:
		return "Execution cancelled", task + " was cancelled."
	case domain.EventExecutionNeedsAttention:
		return "Needs your attention", task + " needs a human to act: " + p["detail"]
	case domain.EventPullRequestCreated:
		return "Draft pull request created", task + " opened " + p["url"]
	case domain.EventPullRequestUpdated:
		return "Draft pull request updated", task + " updated " + p["url"]
	case domain.EventTestFailed:
		return "Tests failed", task + ": tests failed and need review."
	case domain.EventHarnessUnavailable:
		return "Harness unavailable", task + ": " + p["detail"]
	case domain.EventHarnessNoAlternative:
		return "No harness available", task + ": " + p["detail"]
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
