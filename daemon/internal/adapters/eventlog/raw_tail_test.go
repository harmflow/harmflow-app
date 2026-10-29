package eventlog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"regexp/syntax"
	"slices"
	"strings"
	"testing"
	"time"
)

// countingLog returns a log whose redactions of captures add the bytes they redact to the count it returns.
func countingLog(t *testing.T) (*Log, *int) {
	t.Helper()
	l, err := New(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	n := new(int)
	l.redact = func(s string) string {
		*n += len(s)
		return Redact(s)
	}
	return l, n
}

// wholeTail is what a read of a capture's last limit bytes is: from the first line start after them, redacted at once.
func wholeTail(t *testing.T, l *Log, execution string, attempt int, limit int64) (int64, string) {
	t.Helper()
	data, err := os.ReadFile(l.rawPath(execution, attempt))
	if err != nil {
		t.Fatal(err)
	}
	offset := int64(0)
	if limit > 0 && int64(len(data)) > limit {
		offset = int64(len(data)) - limit
		if i := bytes.IndexByte(data[offset:], '\n'); i >= 0 {
			offset += int64(i + 1)
		}
	}
	return offset, Redact(string(data[offset:]))
}

// readWhole reads a capture, fails unless it reads as a redaction of the whole tail, and returns the bytes it redacted.
func readWhole(t *testing.T, l *Log, n *int, execution string, attempt int, limit int64) int {
	t.Helper()
	before := *n
	c, err := l.ReadRaw(execution, attempt, limit)
	if err != nil {
		t.Fatal(err)
	}
	offset, text := wholeTail(t, l, execution, attempt, limit)
	if c.Offset != offset || c.Text != text {
		at := 0
		for at < min(len(c.Text), len(text)) && c.Text[at] == text[at] {
			at++
		}
		t.Fatalf("ReadRaw(%q, %d, %d) = offset %d and %d bytes, want offset %d and %d bytes, a redaction of the whole tail; "+
			"the texts part at byte %d: %q, want %q", execution, attempt, limit, c.Offset, len(c.Text), offset, len(text), at,
			c.Text[at:min(len(c.Text), at+80)], text[at:min(len(text), at+80)])
	}
	return *n - before
}

// writeLines writes lines to w until they hold at least size bytes, and returns how many they hold.
func writeLines(t *testing.T, w io.Writer, first *int, size int) int {
	t.Helper()
	written := 0
	for written < size {
		line := fmt.Sprintf("line %d pushed with ghp_%s and nothing else\n", *first, strings.Repeat("a", 36))
		*first++
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		written += len(line)
	}
	return written
}

// keyLine returns the first line, for edge "BEGIN", or the last, for "END", of a private key of a kind such as "RSA".
// The tests build these lines, so their source holds no key block for the repository's secret scan to report.
func keyLine(edge, kind string) string {
	return "-----" + edge + " " + kind + " PRIVATE KEY-----\n"
}

// A read of a capture that grew redacts the bytes added and the end of the tail before them, not the whole tail, and
// still returns what a redaction of the whole tail does — the tail's start moving with each append, in a capture longer
// than the limit, included.
func TestReadRawRedactsWhatWasAdded(t *testing.T) {
	l, n := countingLog(t)
	w, err := l.RawWriter("e1", 1)
	if err != nil {
		t.Fatal(err)
	}
	const longestLine = 100
	next := 0
	writeLines(t, w, &next, 200<<10)
	info, err := os.Stat(l.rawPath("e1", 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := readWhole(t, l, n, "e1", 1, 0); int64(got) != info.Size() {
		t.Errorf("a first read redacted %d bytes, want the whole capture's %d", got, info.Size())
	}
	if got := readWhole(t, l, n, "e1", 1, 0); got != 0 {
		t.Errorf("a read of an unchanged capture redacted %d bytes, want 0", got)
	}
	for range 5 {
		added := writeLines(t, w, &next, 300)
		if got, most := readWhole(t, l, n, "e1", 1, 0), added+tailPiece+tailOverlap+longestLine; got > most {
			t.Errorf("a read after %d bytes were added redacted %d bytes, want at most %d", added, got, most)
		}
	}

	const limit = 100 << 10
	readWhole(t, l, n, "e1", 1, limit)
	for range 5 {
		added := writeLines(t, w, &next, 300)
		if got, most := readWhole(t, l, n, "e1", 1, limit), added+2*(tailPiece+longestLine)+tailOverlap; got > most {
			t.Errorf("a read of the last %d bytes after %d were added redacted %d bytes, want at most %d", limit, added, got,
				most)
		}
	}
}

// A secret that spans lines, begun before a read and ended after it, is masked by the next read whatever its length.
func TestReadRawMasksASecretAcrossReads(t *testing.T) {
	body := strings.Repeat(strings.Repeat("c2VjcmV0", 8)+"\n", 10)
	blank := strings.Repeat("\n", tailPiece+tailOverlap) // blank lines, which the header's pattern spans
	for _, c := range []struct{ name, before, after, secret string }{
		{"a private key", keyLine("BEGIN", "RSA") + body, body + keyLine("END", "RSA"), "c2VjcmV0"},
		{"a private key longer than a piece", keyLine("BEGIN", "OPENSSH") + strings.Repeat(body, 70),
			body + keyLine("END", "OPENSSH"), "c2VjcmV0"},
		{"an authorization header", "Authorization:\n" + blank, "  Bearer " + strings.Repeat("t0k3n", 6) + "\n", "t0k3n"},
		{"a bearer token lines later", "authorization: bearer\n" + blank, strings.Repeat("t0k3n", 6) + "\n", "t0k3n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, n := countingLog(t)
			w, err := l.RawWriter("e1", 1)
			if err != nil {
				t.Fatal(err)
			}
			next := 0
			writeLines(t, w, &next, 3*tailPiece)
			if _, err := w.Write([]byte(c.before)); err != nil {
				t.Fatal(err)
			}
			readWhole(t, l, n, "e1", 1, 0)
			if _, err := w.Write([]byte(c.after)); err != nil {
				t.Fatal(err)
			}
			writeLines(t, w, &next, tailOverlap)
			readWhole(t, l, n, "e1", 1, 0)
			if got, err := l.ReadRaw("e1", 1, 0); err != nil || strings.Contains(got.Text, c.secret) {
				t.Errorf("ReadRaw after the secret ended = %v, and its text holds %q: want it masked", err, c.secret)
			}
		})
	}
}

// Whatever a capture holds and whenever it is read, from its start or from its last bytes, a read returns what a
// redaction of the whole tail does.
func TestReadRawMatchesAWholeRedaction(t *testing.T) {
	l, n := countingLog(t)
	w, err := l.RawWriter("e1", 1)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	key := func(lines int) string {
		return keyLine("BEGIN", "EC") + strings.Repeat("MHcCAQEEIBkg4LVWM9nuwNSk3yByxZpYRTBnVJk5oCw\n", lines) +
			keyLine("END", "EC")
	}
	parts := []func() string{
		func() string { return fmt.Sprintf("plain output %d\n", r.IntN(1000)) },
		func() string { return "token ghp_" + strings.Repeat("b", 36) + "\n" },
		func() string { return "\n" },
		func() string { return strings.Repeat("a long line of output ", 1+r.IntN(1000)) + "\n" },
		func() string { return key(1 + r.IntN(40)) },
		func() string { return key(470 + r.IntN(100)) }, // longer than a piece and the overlap
		func() string { return keyLine("BEGIN", "RSA") + "MIIEowIBAAKCAQEA\n" },
		func() string { return "MIIEowIBAAKCAQEA" + keyLine("END", "RSA") },
		func() string { return keyLine("END", "RSA") },
		func() string { return "Authorization:\n" },
		func() string { return "  Bearer\n" },
		func() string { return "Authorization: Bearer " + strings.Repeat("z", 20) + "\n" },
		func() string { return strings.Repeat("x9", 12) + "\n" },
		func() string { return "password: hunter2 and Cookie: sid=1\n" },
	}
	for range 250 {
		if _, err := w.Write([]byte(parts[r.IntN(len(parts))]())); err != nil {
			t.Fatal(err)
		}
		if r.IntN(4) == 0 {
			readWhole(t, l, n, "e1", 1, []int64{0, 40 << 10, 90 << 10, 90 << 10, 90 << 10}[r.IntN(5)])
		}
	}
}

// A capture that shrank, or one written again after its log was deleted, is redacted whole, never as an extension of
// the tail read before.
func TestReadRawRedactsANewCaptureWhole(t *testing.T) {
	l, n := countingLog(t)
	w, err := l.RawWriter("e1", 1)
	if err != nil {
		t.Fatal(err)
	}
	next := 0
	writeLines(t, w, &next, 3*tailPiece)
	readWhole(t, l, n, "e1", 1, 0)

	shorter := strings.Repeat("short line\n", 100)
	if err := os.WriteFile(l.rawPath("e1", 1), []byte(shorter), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readWhole(t, l, n, "e1", 1, 0); got != len(shorter) {
		t.Errorf("a read of a capture that shrank redacted %d bytes, want the whole capture's %d", got, len(shorter))
	}

	if err := l.DeleteLog("e1"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.dir+"/e1", 0o700); err != nil {
		t.Fatal(err)
	}
	longer := shorter + strings.Repeat("another line\n", 4000)
	if err := os.WriteFile(l.rawPath("e1", 1), []byte(longer), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readWhole(t, l, n, "e1", 1, 0); got != len(longer) {
		t.Errorf("a read of a capture written after its log was deleted redacted %d bytes, want the whole capture's %d", got,
			len(longer))
	}
}

// The log keeps tails within its budget, dropping the least recently read first.
func TestTailsKeepTheirBudget(t *testing.T) {
	c := tails{m: map[tailKey]*tailEntry{}}
	big := &tail{text: strings.Repeat("x", tailBudget/3+1)}
	c.put(tailKey{"e1", 1}, big)
	c.put(tailKey{"e2", 1}, big)
	c.get(tailKey{"e1", 1})
	c.put(tailKey{"e3", 1}, big)
	if c.get(tailKey{"e2", 1}) != nil || c.get(tailKey{"e1", 1}) == nil || c.get(tailKey{"e3", 1}) == nil {
		t.Errorf("after a third tail over the budget, the tails kept are %v, want e1 and e3", slices.Collect(maps.Keys(c.m)))
	}
	if c.bytes > tailBudget {
		t.Errorf("tails hold %d bytes, want at most %d", c.bytes, tailBudget)
	}
	c.put(tailKey{"e4", 1}, &tail{text: strings.Repeat("x", tailBudget+1)})
	if c.get(tailKey{"e4", 1}) != nil {
		t.Error("a tail longer than the budget was kept")
	}
}

// A tail is cut only where cutPoints and headerOpen find no secret open, and they know the two patterns that can span
// lines: a pattern added that spans lines needs its open form there too.
func TestOnlyTheKeyAndTheHeaderSpanLines(t *testing.T) {
	var spanning []string
	for _, re := range secretPatterns {
		p, err := syntax.Parse(re.String(), syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		if matchesNewline(p) {
			spanning = append(spanning, re.String())
		}
	}
	if len(spanning) != 2 || !strings.Contains(spanning[0], "authorization:") ||
		!strings.HasPrefix(spanning[1], privateKeyBegin) {
		t.Errorf("the patterns that can match a newline are %q, want the authorization header's and the private key's",
			spanning)
	}
}

func matchesNewline(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpAnyChar:
		return true
	case syntax.OpLiteral:
		return slices.Contains(re.Rune, '\n')
	case syntax.OpCharClass:
		for i := 0; i+1 < len(re.Rune); i += 2 {
			if re.Rune[i] <= '\n' && '\n' <= re.Rune[i+1] {
				return true
			}
		}
		return false
	}
	return slices.ContainsFunc(re.Sub, matchesNewline)
}

// Where a cut may fall: never while a private key or an authorization header is open, and a key closes only at a last
// line that starts a line.
func TestCutPoints(t *testing.T) {
	filler := strings.Repeat("ordinary output line\n", (tailPiece+tailOverlap)/21+2)
	for _, c := range []struct {
		name, at string
		cut      bool
	}{
		{"plain text", "plain\n", true},
		{"an open private key", keyLine("BEGIN", "RSA") + "MIIE\n", false},
		{"a closed private key", keyLine("BEGIN", "RSA") + "MIIE\n" + keyLine("END", "RSA"), true},
		{"a key closed inside a line", keyLine("BEGIN", "RSA") + "MIIE" + keyLine("END", "RSA"), false},
		{"an authorization header", "Authorization:\n\n", false},
		{"a bearer header", "AUTHORIZATION: Bearer\n", false},
		{"a header with its token", "Authorization: Bearer abc\n", true},
		{"a word ending in bearer", "Authorization: forbearer\n", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The case's text ends where cutPoints first looks for a cut, at tailPiece.
			data := []byte(strings.Repeat("x", tailPiece-len(c.at)-1) + "\n" + c.at + filler)
			if got := slices.Contains(cutPoints(data), tailPiece); got != c.cut {
				t.Errorf("cutPoints(… %q …) cuts after it: %v, want %v", c.at, got, c.cut)
			}
		})
	}
	if got := cutPoints([]byte(strings.Repeat("x", 2*tailPiece))); got != nil {
		t.Errorf("cutPoints(one line) = %v, want no cut", got)
	}
}

// A capture that reads short, having shrunk while it was read, is read whole as far as it goes; one that cannot be
// read fails the read.
func TestReadTailOfACaptureThatFails(t *testing.T) {
	data := []byte(strings.Repeat("a line of output\n", 3*tailPiece/17))
	last, err := readTail(bytes.NewReader(data), int64(len(data)), 0, nil, Redact)
	if err != nil || len(last.cuts) < 3 {
		t.Fatalf("readTail(%d bytes) = %d pieces, %v, want several", len(data), len(last.cuts), err)
	}
	shorter := data[:len(data)/2]
	for _, from := range []int64{0, int64(len(shorter)) - 100} {
		got, err := readTail(bytes.NewReader(shorter), int64(len(data))+50, from, last, Redact)
		if err != nil {
			t.Fatal(err)
		}
		start := from
		if from > 0 {
			start += int64(lineAfter(shorter[from:]))
		}
		if want := Redact(string(shorter[start:])); got.text != want || got.size != int64(len(shorter)) {
			t.Errorf("readTail from %d of a capture that read short = %d bytes of %d, want %d bytes of %d", from,
				len(got.text), got.size, len(want), len(shorter))
		}
	}
	for _, last := range []*tail{nil, last} {
		if _, err := readTail(failingReader{}, int64(len(data))+50, 0, last, Redact); err == nil {
			t.Errorf("readTail of a capture that cannot be read, from the tail %v, = nil error, want one", last != nil)
		}
	}
}

type failingReader struct{}

func (failingReader) ReadAt([]byte, int64) (int, error) { return 0, errors.New("disk failed") }
