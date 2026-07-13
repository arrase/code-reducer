package security

import "errors"

var (
	// ErrPathTraversal is returned when a path resolves outside the repository root.
	ErrPathTraversal = errors.New("security violation: path traversal detected")
	// ErrLockHeld is returned when another code-reducer process holds the file lock.
	ErrLockHeld      = errors.New("lock is already held by another process")
)
