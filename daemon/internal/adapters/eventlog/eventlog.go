// Package eventlog writes one JSONL file per execution, keeps a buffer of recent events for SSE replay, and
// publishes every event to live subscribers (ADR-004, ADR-007). Secrets are redacted before anything is written.
package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

const ringSize = 20000

type Log struct {
	dir   string
	clock func() time.Time

	mu     sync.Mutex
	lastID int64
	// floor is the id after which every event is still in ring; older ones are only in the files.
	floor   int64
	ring    []domain.Event
	subs    map[int]chan domain.Event
	nextSub int
	files   map[string]*os.File
}

func New(dir string, clock func() time.Time) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{dir: dir, clock: clock, floor: clock().UnixMicro(), subs: map[int]chan domain.Event{},
		files: map[string]*os.File{}}, nil
}

// Append assigns the next incremental id, writes the event to its execution's log, and publishes it. Ids are
// strictly increasing across restarts because they never fall behind the clock in microseconds.
func (l *Log) Append(e domain.Event) domain.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	id := now.UnixMicro()
	if id <= l.lastID {
		id = l.lastID + 1
	}
	l.lastID = id
	e.ID = id
	e.SchemaVersion = domain.EventSchemaVersion
	if e.Timestamp.IsZero() {
		e.Timestamp = now
	}
	if e.Severity == "" {
		e.Severity = domain.SeverityInfo
	}
	e.Message = Redact(e.Message)
	e.Params = redactParams(e.Params)
	e.Payload = redactMap(e.Payload)

	if e.ExecutionID != "" {
		l.write(e)
	}
	l.ring = append(l.ring, e)
	if len(l.ring) > ringSize {
		cut := len(l.ring) - ringSize/2
		l.floor = l.ring[cut-1].ID
		l.ring = append([]domain.Event(nil), l.ring[cut:]...)
	}
	for id, ch := range l.subs {
		select {
		case ch <- e:
		default:
			// A subscriber that cannot keep up is dropped; it reconnects with Last-Event-ID and replays.
			close(ch)
			delete(l.subs, id)
		}
	}
	return e
}

func (l *Log) write(e domain.Event) {
	f, ok := l.files[e.ExecutionID]
	if !ok {
		var err error
		f, err = os.OpenFile(l.path(e.ExecutionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		l.files[e.ExecutionID] = f
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
	if isTerminal(e.Type) {
		_ = f.Sync()
	}
}

// Close releases an execution's log file once it has ended.
func (l *Log) Close(executionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f, ok := l.files[executionID]; ok {
		_ = f.Sync()
		_ = f.Close()
		delete(l.files, executionID)
	}
}

func isTerminal(t string) bool {
	switch t {
	case domain.EventExecutionCompleted, domain.EventExecutionFailed, domain.EventExecutionCancelled,
		domain.EventExecutionInterrupted, domain.EventExecutionNoWork, domain.EventExecutionNeedsAttention:
		return true
	}
	return false
}

func (l *Log) path(executionID string) string { return filepath.Join(l.dir, executionID+".jsonl") }

// Replay returns the stored events of an execution after an id; expired is true when the log was deleted by
// retention (D-073).
func (l *Log) Replay(executionID string, after int64, limit int) ([]domain.Event, bool, error) {
	l.mu.Lock()
	if f, ok := l.files[executionID]; ok {
		_ = f.Sync()
	}
	l.mu.Unlock()
	f, err := os.Open(l.path(executionID))
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Event{}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	out := []domain.Event{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var e domain.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.ID <= after {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, false, sc.Err()
}

// Subscribe returns the buffered events after the given id followed by live ones. replayGap is true when the
// buffer no longer reaches back to that id.
func (l *Log) Subscribe(after int64) (<-chan domain.Event, bool, func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	gap := false
	var backlog []domain.Event
	if after > 0 {
		gap = after < l.floor
		for _, e := range l.ring {
			if e.ID > after {
				backlog = append(backlog, e)
			}
		}
	}
	ch := make(chan domain.Event, len(backlog)+4096)
	for _, e := range backlog {
		ch <- e
	}
	id := l.nextSub
	l.nextSub++
	l.subs[id] = ch
	cancel := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if c, ok := l.subs[id]; ok {
			close(c)
			delete(l.subs, id)
		}
	}
	return ch, gap, cancel
}

func (l *Log) rawPath(executionID string, attempt int) string {
	return filepath.Join(l.dir, executionID, "attempt-"+itoa(attempt)+".raw")
}

// RawWriter captures an attempt's raw output for diagnosis, redacted line by line.
func (l *Log) RawWriter(executionID string, attempt int) (io.WriteCloser, error) {
	path := l.rawPath(executionID, attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &redactingWriter{f: f}, nil
}

// ReadRaw reads the last limit bytes of an attempt's capture, from the start of a line. The text is redacted again,
// whole, so a secret that spans lines is masked too.
func (l *Log) ReadRaw(executionID string, attempt int, limit int64) (ports.RawCapture, error) {
	f, err := os.Open(l.rawPath(executionID, attempt))
	if errors.Is(err, os.ErrNotExist) {
		return ports.RawCapture{}, nil
	}
	if err != nil {
		return ports.RawCapture{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ports.RawCapture{}, err
	}
	size, offset := info.Size(), int64(0)
	if limit > 0 && size > limit {
		offset = size - limit
	}
	data := make([]byte, size-offset)
	n, err := f.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return ports.RawCapture{}, err
	}
	data = data[:n]
	if offset > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data, offset = data[i+1:], offset+int64(i+1)
		}
	}
	return ports.RawCapture{Size: size, Offset: offset, Text: Redact(string(data))}, nil
}

// DeleteLog removes an execution's JSONL and raw output; the execution record is kept (D-073).
func (l *Log) DeleteLog(executionID string) error {
	l.Close(executionID)
	err1 := os.Remove(l.path(executionID))
	err2 := os.RemoveAll(filepath.Join(l.dir, executionID))
	if err1 != nil && !errors.Is(err1, os.ErrNotExist) {
		return err1
	}
	return err2
}

func (l *Log) LogModTime(executionID string) (time.Time, bool) {
	info, err := os.Stat(l.path(executionID))
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// redactingWriter is safe for concurrent use: the process's stdout and stderr are copied into it at the same time.
type redactingWriter struct {
	mu  sync.Mutex
	f   *os.File
	buf []byte
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		if _, err := w.f.WriteString(Redact(string(w.buf[:i+1]))); err != nil {
			return 0, err
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func (w *redactingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		_, _ = w.f.WriteString(Redact(string(w.buf)))
	}
	return w.f.Close()
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`sk-(?:ant-|proj-)?[A-Za-z0-9_\-]{20,}`),
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9\-]{10,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[A-Za-z0-9._\-]{16,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

// Redact masks credentials in text written to logs and events (FS-009 FR-005).
func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			if sub := re.FindStringSubmatch(m); len(sub) > 1 && sub[1] != "" {
				return sub[1] + "[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return s
}

func redactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return Redact(t)
	case map[string]any:
		return redactMap(t)
	case []any:
		c := make([]any, len(t))
		for i := range t {
			c[i] = redactValue(t[i])
		}
		return c
	case []string:
		c := make([]string, len(t))
		for i := range t {
			c[i] = Redact(t[i])
		}
		return c
	case map[string]string:
		return redactParams(t)
	}
	return v
}

// redactParams redacts the params of a text, which may quote a harness's output.
func redactParams(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = Redact(v)
	}
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
