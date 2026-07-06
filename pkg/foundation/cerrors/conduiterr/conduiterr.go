// Copyright © 2026 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package conduiterr defines ConduitError, Conduit's uniform, machine-actionable
// error type. A ConduitError carries a stable Code, a human message, and optional
// fields an agent or UI can act on (failing config path, a suggestion, and a
// structured Fix). It composes with cerrors: it wraps a cause and preserves stack
// traces, so errors.As/Is work as usual.
//
// This package is the Go representation. The wire representation (google.rpc.Status
// with a google.rpc.ErrorInfo detail) and the mapping between the two live in
// status.go, with a round-trip test that keeps the two encodings from drifting.
//
// Construction is only via New, Wrap, or the WithCode escape hatch; a bare
// &ConduitError{} literal carries no stack trace (ConduitError is not itself an
// xerrors value) and is forbidden — enforced outside this package (and outside
// _test.go files) by the forbidigo rule in .golangci.yml.
package conduiterr

import (
	"sort"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"google.golang.org/grpc/codes"
)

// Code is a stable error identity plus the gRPC status category it maps to. The
// category is set explicitly at registration, never inferred from the Reason
// string (substring-matching "not_found" is not naming discipline).
type Code struct {
	reason   string
	grpcCode codes.Code
}

// Reason is the stable, dotted identifier (e.g. "connector.plugin_not_found"). It
// is the source of truth for docs, llms.txt, and UI rendering.
func (c Code) Reason() string { return c.reason }

// GRPCCode is the gRPC status category this Code maps to at the API boundary.
func (c Code) GRPCCode() codes.Code { return c.grpcCode }

func (c Code) String() string { return c.reason }

// IsZero reports whether c is the zero Code (unregistered).
func (c Code) IsZero() bool { return c.reason == "" }

var registry = map[string]Code{}

// Register adds a Code to the global registry and returns it. Each boundary package
// registers the codes it owns, so the registry is the single source of truth for
// docs, llms.txt, and the UI. It panics on a duplicate reason so collisions are
// caught at init, not in production.
//
// Register MUST be called only from a package-level var initializer (as the
// foundational codes below are). The registry is not synchronized; Go's package
// initialization is single-threaded, so init-time registration is race-free, but
// calling Register after program start or from a goroutine is a data race.
func Register(reason string, grpcCode codes.Code) Code {
	if _, ok := registry[reason]; ok {
		panic("conduiterr: duplicate code reason " + reason)
	}
	c := Code{reason: reason, grpcCode: grpcCode}
	registry[reason] = c
	return c
}

// LookupCode returns the registered Code for a reason, and whether it was found.
func LookupCode(reason string) (Code, bool) {
	c, ok := registry[reason]
	return c, ok
}

// Codes returns every registered Code, sorted by reason. Used to generate the
// error-code reference for docs, llms.txt, and the UI.
func Codes() []Code {
	out := make([]Code, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].reason < out[j].reason })
	return out
}

// Foundational codes. More are registered by the packages that own each boundary
// as migration proceeds; these seed the registry and cover the fallbacks.
var (
	// CodeUnknown is the fallback when an un-coded error reaches a boundary.
	CodeUnknown = Register("internal.unknown", codes.Internal)
	// CodeInternal is a coded internal failure.
	CodeInternal = Register("internal.error", codes.Internal)
	// CodeInvalidArgument is a generic invalid-input error.
	CodeInvalidArgument = Register("common.invalid_argument", codes.InvalidArgument)
	// CodeNotFound is a generic not-found error.
	CodeNotFound = Register("common.not_found", codes.NotFound)
	// CodeConnectorPluginNotFound is raised when a referenced connector plugin
	// cannot be located.
	CodeConnectorPluginNotFound = Register("connector.plugin_not_found", codes.NotFound)
	// CodeProcessorPluginNotFound is raised when a referenced processor plugin
	// cannot be located.
	CodeProcessorPluginNotFound = Register("processor.plugin_not_found", codes.NotFound)
)

// Fix is a structured, machine-appliable change that resolves an error. The same
// Fix is applied by the CLI `repair` command and the MCP `repair` tool, so a human
// and an agent get the identical remediation rather than divergent capabilities.
type Fix struct {
	ConfigPath string `json:"configPath,omitempty"` // JSON-pointer to the field to change
	Op         string `json:"op,omitempty"`         // "set" | "remove" | "add"
	Value      string `json:"value,omitempty"`      // new value, for set/add
}

// ConduitError is Conduit's uniform user-facing error. Construct with New or Wrap.
type ConduitError struct {
	Code       Code   // stable identity + gRPC category
	Message    string // human-readable, no secrets
	ConfigPath string // JSON-pointer into the offending config, optional
	Suggestion string // human-readable fix hint, optional
	Fix        *Fix   // structured, machine-appliable fix, optional
	DocsURL    string // optional

	err error // wrapped cause, always non-nil after construction
}

// Error returns this error's Message only, not the wrapped cause's text. Use
// Unwrap (or errors.Is/As) to reach the chain.
func (e *ConduitError) Error() string { return e.Message }

// Unwrap returns the wrapped cause so errors.Is/As and the cerrors stack-trace
// walk traverse the chain.
func (e *ConduitError) Unwrap() error { return e.err }

// New creates a leaf ConduitError with no wrapped cause. It guarantees a non-empty
// stack trace so the error is never trace-less.
//
// The captured frame points at New's caller (the real origination site), not at
// New itself: New calls cerrors.NewWithStackDepth(1, msg), which skips one extra
// wrapper frame beyond what cerrors.New/Errorf capture on their own.
func New(code Code, msg string) *ConduitError {
	return &ConduitError{Code: code, Message: msg, err: cerrors.NewWithStackDepth(1, msg)}
}

// Wrap creates a ConduitError around an existing cause, adding context.
//
// Message replaces (does not concatenate) the cause's text — Error() returns msg,
// and the cause remains reachable via Unwrap. So msg should be the boundary-level,
// user-facing description, not a "context: %w" fragment.
//
// Invariant: a boundary must not shadow an inner code. If cause already carries a
// *ConduitError, the returned error takes that inner Code (and inherits its
// structured fields where the wrap does not set them), so errors.As never surfaces
// a code that hides a more specific inner one. Note this means Wrap's own code
// argument is ignored when an inner ConduitError is present. A boundary that
// genuinely needs to reclassify — the explicit-override escape hatch — should
// use WithCode instead.
func Wrap(code Code, msg string, cause error) *ConduitError {
	e := &ConduitError{Code: code, Message: msg, err: cause}

	var inner *ConduitError
	if cause != nil && cerrors.As(cause, &inner) {
		e.Code = inner.Code // pass the inner code through, do not shadow it
		if e.ConfigPath == "" {
			e.ConfigPath = inner.ConfigPath
		}
		if e.Suggestion == "" {
			e.Suggestion = inner.Suggestion
		}
		if e.Fix == nil {
			e.Fix = inner.Fix
		}
		if e.DocsURL == "" {
			e.DocsURL = inner.DocsURL
		}
	}

	if e.err == nil {
		// Guarantee a frame even if cause was nil. Skip=1 for the same reason as
		// New: attribute the frame to Wrap's caller, not to Wrap itself.
		e.err = cerrors.NewWithStackDepth(1, msg)
	}
	return e
}

// WithCode is the explicit code-override escape hatch. Wrap intentionally
// refuses to shadow an inner ConduitError's code (the no-shadow invariant); that
// is correct for ordinary wrapping, but a boundary that genuinely needs to
// reclassify an error — e.g. surface a generic lower-level code as a more
// specific one at an API boundary — has no way to signal that intent through
// Wrap. WithCode is that signal: it always adopts code, even when err already
// is, or wraps, a *ConduitError with its own code. Use it sparingly and only
// when the override is deliberate; reach for Wrap for ordinary context-adding.
//
// err becomes the wrapped cause (via Unwrap), so errors.Is/As and Get still
// reach it and any inner ConduitError through the chain — only the Code
// on the returned, outermost error changes. The returned error's Message is
// err.Error() (for a *ConduitError cause, ConduitError.Error() returns just the
// inner Message, not the full chain text, so the displayed message is
// unchanged; only the classification is). Structured fields (ConfigPath,
// Suggestion, Fix, DocsURL) are inherited from an inner ConduitError, if any,
// since WithCode overrides the code, not the remediation data.
//
// Like New and Wrap, WithCode guarantees a non-empty stack trace: it captures a
// frame at its caller (via cerrors.NewWithStackDepth) when err is nil or itself
// carries none.
func WithCode(err error, code Code) *ConduitError {
	var msg string
	if err != nil {
		msg = err.Error()
	}
	e := &ConduitError{Code: code, Message: msg, err: err}

	var inner *ConduitError
	if err != nil && cerrors.As(err, &inner) {
		e.ConfigPath = inner.ConfigPath
		e.Suggestion = inner.Suggestion
		e.Fix = inner.Fix
		e.DocsURL = inner.DocsURL
	}

	if e.err == nil {
		// Guarantee a frame even if err was nil, same reasoning as New/Wrap.
		e.err = cerrors.NewWithStackDepth(1, msg)
	}
	return e
}

// Get returns the first *ConduitError in err's chain, if any.
func Get(err error) (*ConduitError, bool) {
	var ce *ConduitError
	if cerrors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}
