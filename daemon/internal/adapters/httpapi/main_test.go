package httpapi

import (
	"testing"

	"go.uber.org/goleak"
)

// A rejection's pending count is written by a time.AfterFunc callback, on a goroutine of its own; none may outlive the
// test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
