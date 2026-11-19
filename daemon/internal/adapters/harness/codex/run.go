package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/harmflow/harmflow-app/daemon/internal/adapters/harness/classify"
	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

// An attempt is one `codex app-server` spoken to over its stdin and stdout, as Codex 0.163.0's protocol has it: the
// parser says hello (initialize, then initialized), starts a thread with the attempt's sandbox and approval policy,
// starts one turn with the instructions, and closes the server's stdin once the turn ends, which ends the server. The
// requests the server makes of its own, its approvals among them, wait for the owner; HarmFlow never answers one by
// itself.

// The ids of the parser's own requests.
const (
	idInitialize int64 = 1
	idThread     int64 = 2
	idTurn       int64 = 3
)

// rpcIn is one line the app server writes: an answer to a request of the parser's (id, result or error), a
// notification (method, params), or a request of its own (id, method, params).
type rpcIn struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type rpcAnswer struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// pending is a request of the server's that waits for the owner.
type pending struct {
	rawID       json.RawMessage
	method      string
	request     domain.ApprovalRequest
	permissions json.RawMessage // what a request for more permissions asks, granted back as it is
}

// parser is the Conversation of one attempt.
type parser struct {
	sessionDir  string // where Codex keeps the session, empty when it runs ephemeral
	model       string // the model that prices the attempt, empty when nothing names it
	modelSource string
	prices      domain.PriceTable // what prices the usage; Prices when it holds none

	// What opens the session: the thread's and the turn's parameters, and the version HarmFlow names itself with.
	thread        map[string]any
	turn          map[string]any
	clientVersion string

	mu        sync.Mutex
	in        io.WriteCloser
	sessionID string
	completed bool
	failure   string
	lastText  string
	reported  bool // a turn reported its usage
	tokens    domain.Tokens
	waiting   bool // the last notice was a wait for the network
	// policySaid is set once the thread's start said its requirements set another approval policy, so Codex's own
	// notice of it is not said twice.
	policySaid bool
	ended      bool // the turn ended, or could not start, and the server's stdin is closed
	requests   map[string]*pending
	order      []string            // the pending requests' keys, oldest first
	changes    map[string][]string // the paths of each file change Codex started, by item id
}

var _ ports.Conversation = (*parser)(nil)

// Begin implements ports.Conversation: it says hello to the app server, and starts the thread once it answers.
func (p *parser) Begin(stdin io.WriteCloser) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.in = stdin
	version := p.clientVersion
	if version == "" {
		version = "0.0.0"
	}
	return p.send(map[string]any{"id": idInitialize, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "harmflow", "title": "HarmFlow", "version": version}}})
}

// send writes one line to the server; p.mu is held.
func (p *parser) send(v any) error {
	if p.in == nil {
		return errors.New("the app server's stdin is not open")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = p.in.Write(append(data, '\n'))
	return err
}

// end closes the server's stdin, which ends the server, and withdraws the requests that still wait; p.mu is held.
func (p *parser) end() []ports.HarnessEvent {
	if p.ended {
		return nil
	}
	p.ended = true
	if p.in != nil {
		_ = p.in.Close()
	}
	var out []ports.HarnessEvent
	for _, key := range p.order {
		if q := p.requests[key]; q != nil {
			r := q.request
			out = append(out, ports.HarnessEvent{Kind: ports.HarnessApprovalWithdrawn, Approval: &r})
		}
	}
	p.requests, p.order = nil, nil
	return out
}

// Pending implements ports.Conversation.
func (p *parser) Pending() []domain.ApprovalRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []domain.ApprovalRequest
	for _, key := range p.order {
		if q := p.requests[key]; q != nil {
			out = append(out, q.request)
		}
	}
	return out
}

// Answer implements ports.Conversation: it sends the owner's decision, in the words of the request's kind. Accepting
// for the session asks Codex to remember the approval for the attempt's session alone: HarmFlow stores none.
func (p *parser) Answer(id string, decision domain.ApprovalDecision) error {
	if !decision.Valid() {
		return domain.TextErr("approval_decision_invalid", "{{decision}} is not a decision HarmFlow offers",
			"decision", string(decision))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	q := p.requests[id]
	if q == nil {
		return domain.TextErr("approval_not_pending", "no approval request {{id}} waits for an answer", "id", id)
	}
	var result any
	switch q.request.Kind {
	case domain.ApprovalPermissions:
		switch decision {
		case domain.ApprovalDecline:
			result = map[string]any{"permissions": map[string]any{}}
		default:
			scope := "turn"
			if decision == domain.ApprovalAcceptForSession {
				scope = "session"
			}
			result = map[string]any{"permissions": q.permissions, "scope": scope}
		}
	default:
		words := map[domain.ApprovalDecision]string{domain.ApprovalAccept: "accept",
			domain.ApprovalAcceptForSession: "acceptForSession", domain.ApprovalDecline: "decline"}
		result = map[string]any{"decision": words[decision]}
	}
	if err := p.send(rpcAnswer{ID: q.rawID, Result: result}); err != nil {
		return domain.TextErr("approval_not_sent", "the answer could not reach {{harness}}: {{detail}}",
			"harness", Name, "detail", err.Error())
	}
	p.forget(id)
	return nil
}

// forget drops a pending request; p.mu is held.
func (p *parser) forget(key string) {
	delete(p.requests, key)
	for i, k := range p.order {
		if k == key {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
}

// Line implements ports.OutputParser.
func (p *parser) Line(raw []byte) []ports.HarnessEvent {
	if len(raw) == 0 {
		return nil
	}
	var m rpcIn
	if err := json.Unmarshal(raw, &m); err != nil {
		p.mu.Lock()
		p.waiting = false
		p.mu.Unlock()
		return []ports.HarnessEvent{{Kind: ports.HarnessWarning, Message: classify.Truncate(string(raw), 2000)}}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case m.Method != "" && len(m.ID) > 0:
		return p.request(m)
	case m.Method != "":
		return p.notification(m.Method, m.Params)
	default:
		return p.answer(m)
	}
}

// answer handles the server's answer to one of the parser's requests; p.mu is held.
func (p *parser) answer(m rpcIn) []ports.HarnessEvent {
	id, _ := strconv.ParseInt(string(m.ID), 10, 64)
	if m.Error != nil {
		switch id {
		case idInitialize, idThread, idTurn:
			p.failure = m.Error.Message
			return p.end()
		}
		return nil
	}
	switch id {
	case idInitialize:
		if err := p.send(map[string]any{"method": "initialized"}); err != nil {
			return p.end()
		}
		if err := p.send(map[string]any{"id": idThread, "method": "thread/start", "params": p.thread}); err != nil {
			return p.end()
		}
	case idThread:
		var r struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			Model           string  `json:"model"`
			ReasoningEffort *string `json:"reasoningEffort"`
			ApprovalPolicy  any     `json:"approvalPolicy"`
		}
		_ = json.Unmarshal(m.Result, &r)
		p.sessionID = r.Thread.ID
		started := ports.HarnessEvent{Kind: ports.HarnessStarted, SessionID: r.Thread.ID, SessionDir: p.sessionDir}
		if r.Model != "" {
			started.Model = r.Model
			if p.modelSource == domain.ModelFromConfig || p.model == "" {
				p.model, p.modelSource = r.Model, domain.ModelFromConfig
			}
		}
		if r.ReasoningEffort != nil {
			started.Effort = *r.ReasoningEffort
		}
		out := []ports.HarnessEvent{started}
		// The requirements an administrator manages can refuse approvals off: Codex then asks for approvals, which wait
		// for the owner.
		if s, ok := r.ApprovalPolicy.(string); ok && s != approvalPolicy {
			p.policySaid = true
			out = append(out, ports.HarnessEvent{Kind: ports.HarnessSettingOverridden,
				Message: fmt.Sprintf("Codex runs with approval_policy %s, which its requirements set in place of %s: a command outside the sandbox waits for the owner's approval", s, approvalPolicy)})
		}
		turn := map[string]any{"threadId": r.Thread.ID}
		for k, v := range p.turn {
			turn[k] = v
		}
		if err := p.send(map[string]any{"id": idTurn, "method": "turn/start", "params": turn}); err != nil {
			return append(out, p.end()...)
		}
		return out
	}
	return nil
}

// request handles a request of the server's own: an approval waits for the owner, and any other is refused, since
// nobody runs to answer it; p.mu is held.
func (p *parser) request(m rpcIn) []ports.HarnessEvent {
	key := strings.Trim(string(m.ID), `"`)
	var params struct {
		ItemID                 string `json:"itemId"`
		Command                string `json:"command"`
		Cwd                    string `json:"cwd"`
		Reason                 string `json:"reason"`
		GrantRoot              string `json:"grantRoot"`
		NetworkApprovalContext *struct {
			Host string `json:"host"`
		} `json:"networkApprovalContext"`
		Permissions json.RawMessage `json:"permissions"`
	}
	_ = json.Unmarshal(m.Params, &params)
	q := &pending{rawID: m.ID, method: m.Method,
		request: domain.ApprovalRequest{ID: key, Cwd: params.Cwd, Reason: classify.Truncate(params.Reason, 2000)}}
	switch m.Method {
	case "item/commandExecution/requestApproval":
		q.request.Kind = domain.ApprovalCommand
		q.request.Command = unwrapShell(params.Command)
		if params.NetworkApprovalContext != nil {
			q.request.NetworkHost = params.NetworkApprovalContext.Host
		}
	case "item/fileChange/requestApproval":
		q.request.Kind = domain.ApprovalFileChange
		q.request.Paths = p.changes[params.ItemID]
		if params.GrantRoot != "" {
			q.request.Paths = append(q.request.Paths, params.GrantRoot)
		}
	case "item/permissions/requestApproval":
		q.request.Kind = domain.ApprovalPermissions
		q.permissions = params.Permissions
		var asked struct {
			FileSystem *struct {
				Read  []string `json:"read"`
				Write []string `json:"write"`
			} `json:"fileSystem"`
			Network *struct {
				Enabled *bool `json:"enabled"`
			} `json:"network"`
		}
		_ = json.Unmarshal(params.Permissions, &asked)
		if asked.FileSystem != nil {
			q.request.Paths = append(append(q.request.Paths, asked.FileSystem.Write...), asked.FileSystem.Read...)
		}
		q.request.Network = asked.Network != nil && asked.Network.Enabled != nil && *asked.Network.Enabled
	default:
		_ = p.send(rpcAnswer{ID: m.ID, Error: &rpcError{Code: -32601,
			Message: "HarmFlow runs Codex unattended and does not answer " + m.Method}})
		return nil
	}
	if p.ended {
		_ = p.send(rpcAnswer{ID: m.ID, Result: map[string]any{"decision": "decline"}})
		return nil
	}
	if p.requests == nil {
		p.requests = map[string]*pending{}
	}
	if p.requests[key] == nil {
		p.order = append(p.order, key)
	}
	p.requests[key] = q
	r := q.request
	return []ports.HarnessEvent{{Kind: ports.HarnessApprovalRequested, Approval: &r}}
}

// notice is a notification's params, of which each method reads its own.
type notice struct {
	Item *appItem `json:"item"`
	Turn *struct {
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"turn"`
	TokenUsage *struct {
		Total *struct {
			InputTokens           int64 `json:"inputTokens"`
			CachedInputTokens     int64 `json:"cachedInputTokens"`
			CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
			OutputTokens          int64 `json:"outputTokens"`
		} `json:"total"`
	} `json:"tokenUsage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	WillRetry bool            `json:"willRetry"`
	Message   string          `json:"message"`
	Summary   string          `json:"summary"`
	Details   string          `json:"details"`
	RequestID json.RawMessage `json:"requestId"`
	ReviewID  string          `json:"reviewId"`
	Review    *struct {
		Status    string  `json:"status"`
		RiskLevel *string `json:"riskLevel"`
		Rationale *string `json:"rationale"`
	} `json:"review"`
	Action *struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Program string `json:"program"`
	} `json:"action"`
}

// appItem is an item of the thread, as the app server writes it.
type appItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregatedOutput"`
	ExitCode         *int   `json:"exitCode"`
	Status           string `json:"status"`
	Text             string `json:"text"`
	Changes          []struct {
		Path string `json:"path"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
	} `json:"changes"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
	Query  string `json:"query"`
}

// notification handles one notification of the server's; p.mu is held.
func (p *parser) notification(method string, raw json.RawMessage) []ports.HarnessEvent {
	var n notice
	_ = json.Unmarshal(raw, &n)
	// Codex repeats the wait every few seconds while it lasts: only the first notice of a stretch is an event, and any
	// other notice of the turn ends the stretch.
	wasWaiting := p.waiting
	if method != "error" {
		p.waiting = false
	}
	switch method {
	case "item/started", "item/completed":
		if n.Item != nil {
			return p.item(method == "item/completed", n.Item)
		}
	case "thread/tokenUsage/updated":
		if n.TokenUsage != nil && n.TokenUsage.Total != nil {
			u := n.TokenUsage.Total
			p.reported = true
			// The total is the thread's so far; as in OpenAI's API, its input includes what was read from and written to
			// the cache, so the plain input is what remains.
			p.tokens = domain.Tokens{Input: max(0, u.InputTokens-u.CachedInputTokens-u.CacheWriteInputTokens),
				CacheWriteShort: u.CacheWriteInputTokens, CacheRead: u.CachedInputTokens, Output: u.OutputTokens}
			return []ports.HarnessEvent{{Kind: ports.HarnessUsage, Usage: p.usage(false)}}
		}
	case "turn/completed":
		if n.Turn != nil {
			switch n.Turn.Status {
			case "completed":
				p.completed = true
			case "interrupted":
				p.failure = firstNonEmpty(p.failure, "the turn was interrupted")
			default:
				if n.Turn.Error != nil && n.Turn.Error.Message != "" {
					p.failure = n.Turn.Error.Message
				} else {
					p.failure = firstNonEmpty(p.failure, "the turn failed")
				}
			}
		}
		return p.end()
	case "error":
		if n.Error == nil {
			return nil
		}
		p.waiting = n.WillRetry && networkWaitRe.MatchString(n.Error.Message)
		// A retry's notice says why the turn struggles; the last one stands as its failure until the turn says otherwise.
		p.failure = firstNonEmpty(n.Error.Message, p.failure)
		if p.waiting && !wasWaiting {
			return []ports.HarnessEvent{{Kind: ports.HarnessNetworkWait, Message: classify.Truncate(n.Error.Message, 2000)}}
		}
	case "warning", "configWarning", "deprecationNotice", "guardianWarning":
		text := strings.TrimSpace(firstNonEmpty(n.Message, n.Summary))
		if n.Details != "" {
			text = strings.TrimSpace(text + " " + n.Details)
		}
		if text == "" {
			return nil
		}
		kind := ports.HarnessWarning
		if overrideRe.MatchString(text) && !(p.policySaid && strings.Contains(text, "`approval_policy`")) {
			kind = ports.HarnessSettingOverridden
		}
		return []ports.HarnessEvent{{Kind: kind, Message: classify.Truncate(text, 2000)}}
	case "serverRequest/resolved":
		key := strings.Trim(string(n.RequestID), `"`)
		if q := p.requests[key]; q != nil {
			p.forget(key)
			r := q.request
			return []ports.HarnessEvent{{Kind: ports.HarnessApprovalWithdrawn, Approval: &r}}
		}
	case "item/autoApprovalReview/started", "item/autoApprovalReview/completed":
		review := &domain.AutoReview{ID: n.ReviewID}
		if n.Action != nil {
			review.Action = n.Action.Type
			review.Command = unwrapShell(firstNonEmpty(n.Action.Command, n.Action.Program))
		}
		if n.Review != nil {
			review.Status = n.Review.Status
			if n.Review.RiskLevel != nil {
				review.Risk = *n.Review.RiskLevel
			}
			if n.Review.Rationale != nil {
				review.Reason = classify.Truncate(*n.Review.Rationale, 2000)
			}
		}
		kind := ports.HarnessAutoReviewStarted
		if strings.HasSuffix(method, "/completed") {
			kind = ports.HarnessAutoReviewCompleted
		}
		return []ports.HarnessEvent{{Kind: kind, Review: review}}
	}
	return nil
}

// item turns one item of the thread into events; p.mu is held.
func (p *parser) item(done bool, it *appItem) []ports.HarnessEvent {
	switch it.Type {
	case "commandExecution":
		if !done {
			return []ports.HarnessEvent{{Kind: ports.HarnessToolStarted, ToolID: it.ID, Tool: "shell", Command: unwrapShell(it.Command)}}
		}
		declined := it.Status == "declined"
		isErr := !declined && (it.Status == "failed" || (it.ExitCode != nil && *it.ExitCode != 0))
		return []ports.HarnessEvent{{Kind: ports.HarnessToolDone, ToolID: it.ID, Tool: "shell", ExitCode: it.ExitCode,
			IsError: isErr, Declined: declined, Output: classify.Truncate(it.AggregatedOutput, 4000)}}
	case "fileChange":
		if !done {
			// Kept for the approval its change may ask for, which names the item and not its files.
			if p.changes == nil {
				p.changes = map[string][]string{}
			}
			paths := make([]string, 0, len(it.Changes))
			for _, c := range it.Changes {
				paths = append(paths, c.Path)
			}
			p.changes[it.ID] = paths
			return nil
		}
		delete(p.changes, it.ID)
		// A change Codex declined, or failed to apply, left the workspace as it was.
		if it.Status == "declined" || it.Status == "failed" {
			return nil
		}
		var out []ports.HarnessEvent
		for _, c := range it.Changes {
			out = append(out, ports.HarnessEvent{Kind: ports.HarnessFileChanged, Path: c.Path, Message: c.Kind.Type})
		}
		return out
	case "agentMessage":
		if done && strings.TrimSpace(it.Text) != "" {
			p.lastText = it.Text
			return []ports.HarnessEvent{{Kind: ports.HarnessMessage, Message: it.Text}}
		}
	case "mcpToolCall", "webSearch", "dynamicToolCall":
		name := firstNonEmpty(it.Tool, it.Type)
		if !done {
			return []ports.HarnessEvent{{Kind: ports.HarnessToolStarted, ToolID: it.ID, Tool: name, Message: firstNonEmpty(it.Query, it.Server)}}
		}
		return []ports.HarnessEvent{{Kind: ports.HarnessToolDone, ToolID: it.ID, Tool: name, IsError: it.Status == "failed",
			Declined: it.Status == "declined"}}
	}
	return nil
}
