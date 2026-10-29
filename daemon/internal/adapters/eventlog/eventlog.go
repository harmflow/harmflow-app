// Package eventlog writes one JSONL file per execution, keeps a buffer of recent events for SSE replay, and
// publishes every event to live subscribers. Secrets are redacted before anything is written.
package eventlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/harmflow/harmflow-app/daemon/internal/domain"
	"github.com/harmflow/harmflow-app/daemon/internal/ports"
)

const ringSize = 20000

// subscriberBuffer is how many live events a subscriber's channel holds beyond its backlog: room for a burst, such as a
// harness's output lines, while an SSE client is between reads. Append never waits for a subscriber. One whose buffer is
// full is dropped, its channel closed, and it reconnects with Last-Event-ID to replay what it missed from the ring, so a
// client that falls behind never holds up the log or the daemon.
const subscriberBuffer = 4096

// Log is the EventLog: the JSONL files of the executions in one directory, a ring of the recent events for replay, and
// the live subscribers.
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
	files   map[string]*logFile
}

// logFile is the open log of a running execution, with an index of where the line of each event it wrote starts, so a
// replay after an id reads only the lines after it. The index lives as long as the file is open: once the execution
// ended, its log no longer grows, and a replay reads it from the start.
type logFile struct {
	f *os.File
	// size is the file's length, which each line written adds to; -1 once it is unknown, and the file has no index.
	size int64
	// ids are the events this log wrote to the file since it opened it, in order, and offsets where each one's line
	// starts. Lines already in the file when it opened, from before a restart or the execution's end, are not in them.
	ids, offsets []int64
}

var _ ports.EventLog = (*Log)(nil)

// New returns the log of the files in dir, creating the directory when it is missing; clock stamps the events and
// gives them their ids.
func New(dir string, clock func() time.Time) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{dir: dir, clock: clock, floor: clock().UnixMicro(), subs: map[int]chan domain.Event{},
		files: map[string]*logFile{}}, nil
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
	lf, ok := l.files[e.ExecutionID]
	if !ok {
		f, err := os.OpenFile(l.path(e.ExecutionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		lf = &logFile{f: f, size: -1}
		if info, err := f.Stat(); err == nil {
			lf.size = info.Size()
		}
		l.files[e.ExecutionID] = lf
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	// The event is in the ring and reaches its subscribers whatever the file does, and Append has no caller that could
	// act on a failed write: the line is lost from the replay only.
	n, err := lf.f.Write(append(data, '\n'))
	switch {
	case err != nil:
		// A line cut short runs into the next one, so no offset after it is known to start a line: the file loses its
		// index, and a replay reads it from the start.
		lf.size, lf.ids, lf.offsets = -1, nil, nil
	case lf.size >= 0:
		lf.ids, lf.offsets = append(lf.ids, e.ID), append(lf.offsets, lf.size)
		lf.size += int64(n)
	}
	if isTerminal(e.Type) {
		_ = lf.f.Sync()
	}
}

// Close releases an execution's log file once it has ended.
func (l *Log) Close(executionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if lf, ok := l.files[executionID]; ok {
		// As in write: a failure loses lines from the replay only, and the execution has ended.
		_ = lf.f.Sync()
		_ = lf.f.Close()
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

// Replay returns the stored events of an execution after an id; expired is true when the log was deleted by retention.
// It reads the file with no lock held, so it never holds up Append, and with no sync: os.File.Write does not buffer,
// so every line Append wrote is in the file already. A running execution's log is read from the line after the id.
func (l *Log) Replay(executionID string, after int64, limit int) ([]domain.Event, bool, error) {
	start := l.start(executionID, after)
	f, err := os.Open(l.path(executionID))
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Event{}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close() // read only: closing cannot lose data
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil, false, err
		}
	}
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

// start is the offset where a replay of an execution's log after an id begins: the line of the first event after it,
// or the end of the file when there is none yet. Every line before it holds an earlier event. It is 0, the start of the
// file, when the log has no index, or when the file may hold an event after the id that the index does not.
func (l *Log) start(executionID string, after int64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, ok := l.files[executionID]
	if !ok || len(lf.ids) == 0 || after < lf.ids[0] {
		return 0
	}
	i, found := slices.BinarySearch(lf.ids, after)
	if found {
		i++
	}
	if i == len(lf.ids) {
		return lf.size
	}
	return lf.offsets[i]
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
	ch := make(chan domain.Event, len(backlog)+subscriberBuffer) // see subscriberBuffer
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
	return filepath.Join(l.dir, executionID, "attempt-"+strconv.Itoa(attempt)+".raw")
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
	defer f.Close() // read only: closing cannot lose data
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

// DeleteLog removes an execution's JSONL and raw output; the execution record is kept.
func (l *Log) DeleteLog(executionID string) error {
	l.Close(executionID)
	err1 := os.Remove(l.path(executionID))
	err2 := os.RemoveAll(filepath.Join(l.dir, executionID))
	if err1 != nil && !errors.Is(err1, os.ErrNotExist) {
		return err1
	}
	return err2
}

// LogModTime implements ports.EventLog: when an execution's JSONL was last written, which ages it for retention; false
// when it has none.
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
	var err error
	if len(w.buf) > 0 {
		_, err = w.f.WriteString(Redact(string(w.buf)))
	}
	return errors.Join(err, w.f.Close())
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`sk-(?:ant-|proj-)?[A-Za-z0-9_\-]{20,}`),
	regexp.MustCompile(`xox[abprs]-[A-Za-z0-9\-]{10,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)(authorization:\s*bearer\s+)[A-Za-z0-9._\-]{16,}`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	// A password, cookie, or session value counts only after its key and separator, on the same line: the words alone,
	// or inside another word, are ordinary text. A value stops before a quote or a backslash, so a masked value never
	// closes the string that holds it, in a task's YAML or anywhere else.
	regexp.MustCompile(`(?i)(\b(?:password|passwd|pwd)\b[ \t]*[:=][ \t]*["']?)[^\s"'\\]+`),
	regexp.MustCompile(`(?i)(\b(?:set-)?cookie[ \t]*:[ \t]*)[^\r\n"'\\]+`),
	regexp.MustCompile(`(?i)(\b(?:sessionid|session|sid)\b[ \t]*[:=][ \t]*["']?)[^\s"'\\;&,]+`),
}

// Redact masks credentials in text written to logs and events.
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

// RedactFields redacts, in place, every string reachable from ptr — through struct fields, slices, arrays, maps,
// pointers, and interfaces — and reports whether any changed. The agent's and the harness's text reaches the daemon as
// such values; redacted as they arrive, nothing the daemon keeps or relays from them holds a credential, and a field
// trimmed later cannot cut a secret short of its pattern. Unexported fields are left alone,
// and the values must hold no pointer cycle.
func RedactFields(ptr any) bool {
	v := reflect.ValueOf(ptr)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return false
	}
	return redactReflect(v.Elem())
}

func redactReflect(v reflect.Value) bool {
	changed := false
	switch v.Kind() {
	case reflect.String:
		if s := v.String(); v.CanSet() {
			if r := Redact(s); r != s {
				v.SetString(r)
				changed = true
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			changed = redactReflect(v.Elem())
		}
	case reflect.Interface:
		// What an interface holds cannot be set in place: redact a copy and put it back.
		if !v.IsNil() && v.CanSet() {
			c := reflect.New(v.Elem().Type()).Elem()
			c.Set(v.Elem())
			if redactReflect(c) {
				v.Set(c)
				changed = true
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanSet() {
				changed = redactReflect(f) || changed
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			changed = redactReflect(v.Index(i)) || changed
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			c := reflect.New(it.Value().Type()).Elem()
			c.Set(it.Value())
			if redactReflect(c) {
				v.SetMapIndex(it.Key(), c)
				changed = true
			}
		}
	}
	return changed
}
