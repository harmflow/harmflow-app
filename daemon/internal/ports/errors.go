package ports

import "errors"

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict means the file changed since the revision an edit started from (FS-001 FR-005).
	ErrConflict = errors.New("conflict")
)
