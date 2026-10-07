package gomedicationcourse

import (
	"errors"
	"fmt"
)

// ConflictError signals a rejected operation. It is returned when a request
// carries a stale prescription version, targets a terminal course, collides
// with an idempotency key that was used with different content, or otherwise
// violates the consistency rules of the course lifecycle.
//
// Callers can detect every rejection branch with errors.Is and switch on the
// machine readable Kind for presentation.
type ConflictError struct {
	Kind    ConflictKind
	Message string
}

// ConflictKind enumerates the stable conflict categories.
type ConflictKind string

const (
	// ConflictVersion indicates a request was based on a prescription
	// version that is no longer the one governing the targeted time point.
	ConflictVersion ConflictKind = "version_conflict"
	// ConflictStatus indicates the course lifecycle state forbids the
	// operation, for example adjusting a completed or cancelled course.
	ConflictStatus ConflictKind = "status_conflict"
	// ConflictEffectiveTime indicates an effective time is earlier than an
	// already confirmed intake fact and therefore cannot be honoured.
	ConflictEffectiveTime ConflictKind = "effective_time_conflict"
	// ConflictIdempotency indicates an external request id was already
	// committed for a different payload.
	ConflictIdempotency ConflictKind = "idempotency_conflict"
	// ConflictDuplicate indicates an intake fact for the time point already
	// exists.
	ConflictDuplicate ConflictKind = "duplicate_conflict"
	// ConflictInvalid indicates malformed or logically impossible input.
	ConflictInvalid ConflictKind = "invalid_request"
)

func (e *ConflictError) Error() string { return string(e.Kind) + ": " + e.Message }

// NewConflict builds a ConflictError.
func NewConflict(kind ConflictKind, format string, args ...any) *ConflictError {
	return &ConflictError{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// AsConflict extracts a *ConflictError from err, if present.
func AsConflict(err error) (*ConflictError, bool) {
	var ce *ConflictError
	return ce, errors.As(err, &ce)
}

// IsConflict reports whether err (or any error in its chain) is a ConflictError
// of the given kind.
func IsConflict(err error, kind ConflictKind) bool {
	var ce *ConflictError
	if errors.As(err, &ce) {
		return ce.Kind == kind
	}
	return false
}
