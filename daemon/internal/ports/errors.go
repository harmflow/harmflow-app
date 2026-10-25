package ports

import "errors"

var (
	// ErrNotFound means the stored record asked for does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the file changed since the revision an edit started from.
	ErrConflict = errors.New("conflict")
)
