//go:build !darwin

package procutil

import (
	"errors"
	"time"
)

// The process table is read on macOS only; elsewhere EndTree ends the harness's group.
const readsProcessTable = false

var errNoProcessTable = errors.New("the process table is read on macOS only")

func processTable() (map[int]procInfo, error) { return nil, errNoProcessTable }

func processInfo(int) (procInfo, bool) { return procInfo{}, false }

func startTime(int) (time.Time, error) { return time.Time{}, errNoProcessTable }

func carries(int, []byte) bool { return false }
