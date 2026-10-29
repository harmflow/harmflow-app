package eventlog

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
)

// An active execution's detail reads its captures again every few seconds, and redacting a whole tail costs what its
// size costs. So the log keeps each attempt's tail as it last returned it, in pieces it redacted apart, and the next
// read redacts again only the last piece, with the bytes added since, and the first one when the tail's start moved
// into it.
//
// A tail is cut into pieces only at a line start where no secret is open. The capture was redacted line by line as it
// was written, so a secret that never spans lines is masked on either side of such a cut. Of the patterns that span
// lines, none is open there: no private key's first line is without its last line, and no authorization header waits
// for its bearer token. Redacting the pieces apart then gives the text a redaction of the whole tail gives, however
// long the secret: the last piece reaches back to before any secret still open.
const (
	// tailPiece is about how many bytes of a tail are redacted apart.
	tailPiece = 16 << 10
	// tailOverlap is the least a tail's last piece holds: the bytes before the end that are redacted again with the ones
	// appended after them.
	tailOverlap = 4 << 10
	// tailBudget is the most text the log keeps for the tails; the least recently read go first.
	tailBudget = 16 << 20
)

const (
	privateKeyBegin = `-----BEGIN [A-Z ]*PRIVATE KEY-----`
	privateKeyEnd   = `-----END [A-Z ]*PRIVATE KEY-----`
)

var (
	keyBegin = regexp.MustCompile(privateKeyBegin)
	keyEnd   = regexp.MustCompile(privateKeyEnd)
)

// spaces is what `\s` matches.
const spaces = " \t\n\f\r"

type tailKey struct {
	execution string
	attempt   int
}

// tail is the end of an attempt's capture as ReadRaw returned it. It is never changed once made.
type tail struct {
	size   int64 // the bytes of the capture read
	from   int64 // where the tail was asked to start
	offset int64 // where text starts in the capture: the first line start after from
	text   string
	// cuts are where the pieces start, in the capture and in text: the first at offset, every other at a line start where
	// no secret is open. The last piece, to size, is the one a longer capture redacts again.
	cuts []tailCut
}

type tailCut struct {
	at   int64
	text int
}

type tailPieceText struct {
	at   int64
	text string
}

// tails holds the last tail of each attempt read, within tailBudget.
type tails struct {
	mu    sync.Mutex
	m     map[tailKey]*tailEntry
	bytes int
	reads uint64
}

type tailEntry struct {
	tail *tail
	read uint64
}

func (c *tails) get(k tailKey) *tail {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok {
		return nil
	}
	c.reads++
	e.read = c.reads
	return e.tail
}

func (c *tails) put(k tailKey, t *tail) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(k)
	if len(t.text) > tailBudget {
		return
	}
	c.reads++
	c.m[k] = &tailEntry{tail: t, read: c.reads}
	c.bytes += len(t.text)
	for c.bytes > tailBudget {
		var oldest tailKey
		var read uint64
		for k, e := range c.m {
			if read == 0 || e.read < read {
				oldest, read = k, e.read
			}
		}
		c.dropLocked(oldest)
	}
}

func (c *tails) drop(k tailKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(k)
}

// forget drops the tails of an execution's attempts.
func (c *tails) forget(execution string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if k.execution == execution {
			c.dropLocked(k)
		}
	}
}

func (c *tails) dropLocked(k tailKey) {
	if e, ok := c.m[k]; ok {
		c.bytes -= len(e.tail.text)
		delete(c.m, k)
	}
}

// readTail reads a capture of size bytes from from, starting at the first line start after from when from is not 0, and
// redacts it. last, when not nil, is the tail the same capture gave before: a capture that grew, read from the same
// point or later, redacts again only what changed. A capture that shrank, or a tail that starts sooner, is redacted
// whole.
func readTail(r io.ReaderAt, size, from int64, last *tail, redact func(string) string) (*tail, error) {
	if last != nil && last.size == size && last.from == from {
		return last, nil
	}
	if last != nil && size >= last.size && from >= last.from {
		if t, ok, err := extendTail(r, size, from, last, redact); err != nil || ok {
			return t, err
		}
	}
	data, err := readAt(r, from, size)
	if err != nil {
		return nil, err
	}
	offset := from
	if from > 0 {
		offset += int64(lineAfter(data))
	}
	return assemble(from, offset, nil, offset, data[offset-from:], redact), nil
}

// extendTail is readTail from last. It keeps last's pieces from the tail's new start on, redacts again the piece that
// start falls in, and redacts again the last piece with the bytes added after it. ok is false when the capture read
// short: it shrank, and the caller reads it whole.
func extendTail(r io.ReaderAt, size, from int64, last *tail, redact func(string) string) (*tail, bool, error) {
	cuts, n := last.cuts, len(last.cuts)
	pieceText := func(i int) string { return last.text[cuts[i].text:cuts[i+1].text] }
	offset := int64(0)
	var head []byte // the capture from from to the end of the piece that holds from
	if from > 0 {
		// The first line start after from is in the piece that holds from: every piece but the last ends at a line start.
		to := size
		for _, c := range cuts[1:] {
			if c.at > from {
				to = c.at
				break
			}
		}
		var err error
		if head, err = readAt(r, from, to); err != nil || int64(len(head)) < to-from {
			return nil, false, err
		}
		offset = from + int64(lineAfter(head))
	}
	i := 0 // the piece offset falls in
	for i+1 < n && cuts[i+1].at <= offset {
		i++
	}
	redo := offset
	var kept []tailPieceText
	if i < n-1 {
		first := tailPieceText{at: offset, text: pieceText(i)}
		if cuts[i].at < offset {
			// The tail starts inside this piece, so from is in it too, and head holds the rest of the piece.
			first.text = redact(string(head[offset-from:]))
		}
		kept = append(kept, first)
		for k := i + 1; k < n-1; k++ {
			kept = append(kept, tailPieceText{at: cuts[k].at, text: pieceText(k)})
		}
		redo = cuts[n-1].at
	}
	data, err := readAt(r, redo, size)
	if err != nil || int64(len(data)) < size-redo {
		return nil, false, err
	}
	return assemble(from, offset, kept, redo, data, redact), true, nil
}

// assemble makes the tail of the kept pieces followed by data, the capture's bytes from redo, which it cuts into pieces
// and redacts piece by piece.
func assemble(from, offset int64, kept []tailPieceText, redo int64, data []byte, redact func(string) string) *tail {
	pieces, at := kept, 0
	for _, c := range cutPoints(data) {
		pieces = append(pieces, tailPieceText{at: redo + int64(at), text: redact(string(data[at:c]))})
		at = c
	}
	pieces = append(pieces, tailPieceText{at: redo + int64(at), text: redact(string(data[at:]))})
	t := &tail{size: redo + int64(len(data)), from: from, offset: offset}
	var b strings.Builder
	for _, p := range pieces {
		t.cuts = append(t.cuts, tailCut{at: p.at, text: b.Len()})
		b.WriteString(p.text)
	}
	t.text = b.String()
	return t
}

// cutPoints returns where data, a capture's bytes from a point where no secret is open, may be cut into pieces that
// redact apart: a line start about every tailPiece bytes, none in the last tailOverlap bytes, and none where a secret
// that spans lines is open.
func cutPoints(data []byte) []int {
	begins, ends := keyBegin.FindAllIndex(data, -1), keyEnd.FindAllIndex(data, -1)
	var cuts []int
	// A private key is open from its first line to the next last line that starts a line. The pattern also ends a key at
	// a last line inside another line, which another pattern may have masked first, so only one that starts a line
	// closes it here: never later than the pattern does.
	open, resume := false, 0
	for next := tailPiece; next <= len(data)-tailOverlap; {
		i := bytes.IndexByte(data[next-1:], '\n')
		if i < 0 {
			break
		}
		c := next + i
		if c > len(data)-tailOverlap {
			break
		}
		for {
			if !open && len(begins) > 0 && begins[0][1] <= c {
				if begins[0][0] >= resume {
					open, resume = true, begins[0][1]
				}
				begins = begins[1:]
			} else if open && len(ends) > 0 && ends[0][1] <= c {
				if e := ends[0][0]; e >= resume && (e == 0 || data[e-1] == '\n') {
					open, resume = false, ends[0][1]
				}
				ends = ends[1:]
			} else {
				break
			}
		}
		if open || headerOpen(data[:c]) {
			next = c + 1
			continue
		}
		cuts = append(cuts, c)
		next = c + tailPiece
	}
	return cuts
}

// headerOpen reports whether text ends in an authorization header whose bearer token is still to come, which the
// header's pattern joins to the lines after it.
func headerOpen(text []byte) bool {
	text = bytes.TrimRight(text, spaces)
	if hasSuffixFold(text, "bearer") {
		text = bytes.TrimRight(text[:len(text)-len("bearer")], spaces)
	}
	return hasSuffixFold(text, "authorization:")
}

// hasSuffixFold reports whether b ends with s, an ASCII word, in any case. The words it is given have no letter that
// folds to a letter outside ASCII, as k and s do.
func hasSuffixFold(b []byte, s string) bool {
	return len(b) >= len(s) && bytes.EqualFold(b[len(b)-len(s):], []byte(s))
}

// lineAfter returns where the first line after data's first byte starts: past the first newline, or 0 when there is
// none.
func lineAfter(data []byte) int {
	return bytes.IndexByte(data, '\n') + 1
}

// readAt reads a capture from from to to; it reads short when the capture is shorter.
func readAt(r io.ReaderAt, from, to int64) ([]byte, error) {
	data := make([]byte, to-from)
	n, err := r.ReadAt(data, from)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return data[:n], nil
}
