package model

import "errors"

var (
	// ErrNotFound is returned when a requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a write would violate uniqueness, e.g. a
	// second active freeze for the same target.
	ErrConflict = errors.New("conflict")
	// ErrInvalid is returned for malformed input.
	ErrInvalid = errors.New("invalid input")
	// ErrReasonRequired is returned when a bypass has no reason.
	ErrReasonRequired = errors.New("reason is required")
)
