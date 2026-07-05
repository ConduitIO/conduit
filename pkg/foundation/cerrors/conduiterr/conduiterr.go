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
// Construction is only via New or Wrap; a bare &ConduitError{} literal carries no
// stack trace (ConduitError is not itself an xerrors value) and is forbidden.
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

// register adds a Code to the global registry. It panics on a duplicate reason so
// collisions are caught at init, not in production.
func register(reason string, grpcCode codes.Code) Code {
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
	CodeUnknown = register("internal.unknown", codes.Internal)
	// CodeInternal is a coded internal failure.
	CodeInternal = register("internal.error", codes.Internal)
	// CodeInvalidArgument is a generic invalid-input error.
	CodeInvalidArgument = register("common.invalid_argument", codes.InvalidArgument)
	// CodeNotFound is a generic not-found error.
	CodeNotFound = register("common.not_found", codes.NotFound)
	// CodeConnectorPluginNotFound is raised when a referenced connector plugin
	// cannot be located.
	CodeConnectorPluginNotFound = register("connector.plugin_not_found", codes.NotFound)
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
	Code       Code
	Message    string
	ConfigPath string // JSON-pointer into the offending config, optional
	Suggestion string // human-readable fix hint, optional
	Fix        *Fix   // structured, machine-appliable fix, optional
	DocsURL    string // optional

	err error // wrapped cause, always non-nil after construction
}

func (e *ConduitError) Error() string { return e.Message }

func (e *ConduitError) Unwrap() error { return e.err }

// New creates a leaf ConduitError with no wrapped cause. It still captures a stack
// frame at the call site so the error is traceable.
func New(code Code, msg string) *ConduitError {
	return &ConduitError{Code: code, Message: msg, err: cerrors.New(msg)}
}

// Wrap creates a ConduitError around an existing cause, adding context.
//
// Invariant: a boundary must not shadow an inner code. If cause already carries a
// *ConduitError, the returned error takes that inner Code (and inherits its
// structured fields where the wrap does not set them), so errors.As never surfaces
// a code that hides a more specific inner one.
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
		e.err = cerrors.New(msg) // guarantee a frame even if cause was nil
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
