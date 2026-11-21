package domain

// ApprovalKind is what a harness asks the owner to approve.
type ApprovalKind string

// The kinds of an approval request: a command outside the sandbox, a change of files, or more permissions for the turn.
const (
	ApprovalCommand     ApprovalKind = "command"
	ApprovalFileChange  ApprovalKind = "file_change"
	ApprovalPermissions ApprovalKind = "permissions"
)

// ApprovalDecision is the owner's answer to an approval request. HarmFlow offers no other: a harness's own decision to
// cancel the turn is not one, since the owner cancels the execution instead.
type ApprovalDecision string

// The decisions the owner can give: accept once, accept for the rest of the attempt's session, or decline.
const (
	ApprovalAccept           ApprovalDecision = "accept"
	ApprovalAcceptForSession ApprovalDecision = "accept_for_session"
	ApprovalDecline          ApprovalDecision = "decline"
)

// Valid reports whether d is one of the decisions the owner can give.
func (d ApprovalDecision) Valid() bool {
	switch d {
	case ApprovalAccept, ApprovalAcceptForSession, ApprovalDecline:
		return true
	}
	return false
}

// ApprovalRequest is one request of a running harness that waits for the owner: what it asks, in the harness's own
// words. ID is the harness's request identifier, unique within the attempt.
type ApprovalRequest struct {
	ID      string       `json:"id"`
	Kind    ApprovalKind `json:"kind"`
	Command string       `json:"command,omitempty"`
	Cwd     string       `json:"cwd,omitempty"`
	Reason  string       `json:"reason,omitempty"`
	// NetworkHost is the host the command wants to reach, when the harness names one.
	NetworkHost string `json:"networkHost,omitempty"`
	// Paths are the files a change touches, or the folders more permissions name.
	Paths []string `json:"paths,omitempty"`
	// Network says a request for more permissions asks for network access.
	Network bool `json:"network,omitempty"`
}

// Payload is the request as an event's payload carries it.
func (q ApprovalRequest) Payload() map[string]any {
	p := map[string]any{"requestId": q.ID, "kind": string(q.Kind)}
	for k, v := range map[string]string{"command": q.Command, "cwd": q.Cwd, "reason": q.Reason,
		"networkHost": q.NetworkHost} {
		if v != "" {
			p[k] = v
		}
	}
	if len(q.Paths) > 0 {
		p["paths"] = q.Paths
	}
	if q.Network {
		p["network"] = true
	}
	return p
}

// AutoReview is one review that a harness's own automatic reviewer of approvals started or ended: HarmFlow records it
// and never answers for it.
type AutoReview struct {
	ID      string `json:"id"`
	Action  string `json:"action,omitempty"`
	Status  string `json:"status,omitempty"`
	Risk    string `json:"risk,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Command string `json:"command,omitempty"`
}
