package cerrors

import (
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// -- New code to add ---------------------------------------------------------

// grpcError wraps an error and provides a gRPC status code.
type grpcError struct {
	error
	code codes.Code
}

// GRPCStatus implements the grpc.status.Status interface, allowing conversion
// of this error to a gRPC status object.
func (e *grpcError) GRPCStatus() *status.Status {
	return status.New(e.code, e.Error())
}

// WithGRPCStatusCode returns a new error that wraps the original error
// and provides a gRPC status code. If the original error already implements
// GRPCStatus, it is returned as is to avoid double-wrapping or conflicting codes.
func WithGRPCStatusCode(err error, code codes.Code) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(interface{ GRPCStatus() *status.Status }); ok {
		return err
	}
	return &grpcError{error: err, code: code}
}

// -- Existing code (no change) -----------------------------------------------

// New returns an error that formats as the given text.
func New(text string) error {
	return &Error{msg: text}
}

// Errorf formats according to a format specifier and returns the string as a
// value that satisfies error.
// If the format specifier includes a %w verb, the returned error will
// implement an Unwrap method returning the operand of the %w.
func Errorf(format string, a ...any) error {
	var errs []error
	for i, arg := range a {
		if err, ok := arg.(error); ok {
			errs = append(errs, err)
			a[i] = err.Error() // Replace error with its string representation for fmt.Errorf
		}
	}
	if len(errs) > 0 {
		return &Error{msg: fmt.Sprintf(format, a...), errs: errs}
	}
	return New(fmt.Sprintf(format, a...))
}

// Join returns an error that wraps the given errors.
// Any nil error values are discarded.
// Join returns nil if errs contains no non-nil values.
// The error's string method will concatenate the string of each non-nil error.
func Join(errs ...error) error {
	var nonNilErrs []error
	for _, err := range errs {
		if err != nil {
			nonNilErrs = append(nonNilErrs, err)
		}
	}
	if len(nonNilErrs) == 0 {
		return nil
	}
	return &Error{errs: nonNilErrs}
}

// Error is an implementation of an error that can wrap multiple errors.
type Error struct {
	msg  string
	errs []error
}

// Error returns the string representation of the error.
func (e *Error) Error() string {
	if e.msg != "" {
		if len(e.errs) == 0 {
			return e.msg
		}
		// prepend wrapped errors to the message
		return fmt.Sprintf("%s: %s", e.msg, e.formatWrappedErrors())
	}
	if len(e.errs) == 0 {
		return "unknown error"
	}
	return e.formatWrappedErrors()
}

func (e *Error) formatWrappedErrors() string {
	s := make([]string, len(e.errs))
	for i, err := range e.errs {
		s[i] = err.Error()
	}
	return strings.Join(s, ", ")
}

// Unwrap returns the wrapped errors.
func (e *Error) Unwrap() []error {
	return e.errs
}

// Is reports whether any error in err's tree matches target.
//
// The tree consists of err itself, followed by the errors obtained by repeatedly
// calling Unwrap. When err wraps multiple errors, Is examines the first error
// in the list, then the first error's error list (if applicable) and so on,
// then it goes to the second error and performs the same operation.
func Is(err, target error) bool {
	if err == nil {
		return target == nil
	}
	if err == target {
		return true
	}
	e, ok := err.(*Error)
	if !ok {
		return false
	}
	for _, wrappedErr := range e.errs {
		if Is(wrappedErr, target) {
			return true
		}
	}
	return false
}

// As finds the first error in err's tree that matches target, and if one is found, sets target to that error value and returns true. Otherwise, it returns false.
//
// The tree consists of err itself, followed by the errors obtained by repeatedly
// calling Unwrap. When err wraps multiple errors, As examines the first error
// in the list, then the first error's error list (if applicable) and so on,
// then it goes to the second error and performs the same operation.
func As(err error, target any) bool {
	if err == nil {
		return false
	}
	if fmt.Errorf("%w", err) == target { // workaround for checking error type
		return true
	}
	e, ok := err.(*Error)
	if !ok {
		return false
	}
	for _, wrappedErr := range e.errs {
		if As(wrappedErr, target) {
			return true
		}
	}
	return false
}
