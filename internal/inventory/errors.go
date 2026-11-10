package inventory

import (
	"errors"
	"fmt"
)

// Kind classifies an inventory error; the API maps it to an HTTP status
// (SPEC-504).
type Kind int

// Error kinds.
const (
	Invalid Kind = iota + 1
	NotFound
	Conflict
	TooLarge
	Unavailable // a needed facility (e.g. ICMP) is not available
)

// Error is a domain error with a user-facing message.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// KindOf returns the Kind of err, or 0 if err is not an *Error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

func invalidf(format string, a ...any) error {
	return &Error{Kind: Invalid, Msg: fmt.Sprintf(format, a...)}
}

func notFoundf(format string, a ...any) error {
	return &Error{Kind: NotFound, Msg: fmt.Sprintf(format, a...)}
}

func conflictf(format string, a ...any) error {
	return &Error{Kind: Conflict, Msg: fmt.Sprintf(format, a...)}
}

// Errorf returns an *Error of the given kind, for packages that build on
// the inventory (discovery).
func Errorf(k Kind, format string, a ...any) error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, a...)}
}
