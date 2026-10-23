package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
)

// The reasons guard rejects a request for; each is recorded on its own.
const (
	reasonMissingToken    = "missing token"
	reasonInvalidToken    = "invalid token"
	reasonForbiddenHost   = "forbidden Host"
	reasonForbiddenOrigin = "forbidden Origin"
)

// defaultRejectionWindow is the least time between two entries for the same reason, unless Server.RejectionWindow
// sets another.
const defaultRejectionWindow = time.Minute

// maxLoggedPath bounds the path an entry holds: under a forbidden Host or Origin, it is any text the caller sent.
const maxLoggedPath = 256

// rejections records the requests guard rejects in daemon.log (FS-009 FR-003): the first at once, then at most one
// entry per reason per rejection window, counting the requests it stands for, so a flood cannot grow the log without
// bound. An entry names the last request's method and path, never its headers or query, so never the token it
// presented.
type rejections struct {
	mu       sync.Mutex
	byReason map[string]*rejected
}

// rejected is one reason's requests since its last entry, and when that entry was written.
type rejected struct {
	count        int
	since        time.Time
	method, path string
	written      time.Time
	timer        *time.Timer // writes the count once the rejection window has passed, if no request does first
}

func (s *Server) rejectionWindow() time.Duration {
	if s.RejectionWindow == 0 {
		return defaultRejectionWindow
	}
	return s.RejectionWindow
}

func (s *Server) now() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock.Now()
}

// reject records the request, then writes the error.
func (s *Server) reject(w http.ResponseWriter, r *http.Request, reason string, status int, code string,
	text domain.Text) {
	s.record(reason, r.Method, r.URL.Path)
	writeError(w, status, code, text)
}

func (s *Server) record(reason, method, path string) {
	if s.Logger == nil {
		return
	}
	now := s.now()
	s.rejected.mu.Lock()
	defer s.rejected.mu.Unlock()
	if s.rejected.byReason == nil {
		s.rejected.byReason = map[string]*rejected{}
	}
	p := s.rejected.byReason[reason]
	if p == nil {
		p = &rejected{}
		s.rejected.byReason[reason] = p
	}
	if p.count == 0 {
		p.since = now
	}
	p.count++
	p.method, p.path = method, path
	s.settle(reason, p, now)
}

// settle writes p's entry once the rejection window has passed since the last one, or else waits for it to pass. The
// caller holds s.rejected.mu.
func (s *Server) settle(reason string, p *rejected, now time.Time) {
	if p.count == 0 {
		return
	}
	window := s.rejectionWindow()
	if p.written.IsZero() || now.Sub(p.written) >= window {
		s.write(reason, p, now)
		return
	}
	if p.timer == nil {
		p.timer = time.AfterFunc(p.written.Add(window).Sub(now), func() {
			s.rejected.mu.Lock()
			defer s.rejected.mu.Unlock()
			p.timer = nil
			s.settle(reason, p, s.now())
		})
	}
}

func (s *Server) write(reason string, p *rejected, now time.Time) {
	path := p.path
	if len(path) > maxLoggedPath {
		path = path[:maxLoggedPath] + "…"
	}
	requests := "requests"
	if p.count == 1 {
		requests = "request"
	}
	s.Logger.Printf("local API rejected %s %q: %s (%d %s since %s)", p.method, path, reason, p.count, requests,
		p.since.Format(time.RFC3339))
	p.count, p.written = 0, now
}

// flushRejections writes what each reason has not written yet, as the daemon stops.
func (s *Server) flushRejections() {
	if s.Logger == nil {
		return
	}
	now := s.now()
	s.rejected.mu.Lock()
	defer s.rejected.mu.Unlock()
	for reason, p := range s.rejected.byReason {
		if p.timer != nil {
			p.timer.Stop()
			p.timer = nil
		}
		if p.count > 0 {
			s.write(reason, p, now)
		}
	}
}
