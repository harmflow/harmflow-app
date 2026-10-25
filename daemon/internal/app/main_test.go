package app

import (
	"testing"

	"go.uber.org/goleak"
)

// The App starts goroutines: its loops, each execution and its progress ticker, and harness detection. None may outlive
// the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
