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

package conduiterr

import (
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	json "github.com/goccy/go-json"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

// errorDomain is the google.rpc.ErrorInfo domain for all Conduit error codes.
const errorDomain = "conduit"

// valid coerces s to valid UTF-8. proto string fields (the status message and the
// ErrorInfo metadata) require valid UTF-8; a single stray byte from config-derived
// input would otherwise fail the detail's marshal and silently drop the entire
// structured error. Replacing invalid bytes with U+FFFD keeps the error intact.
func valid(s string) string { return strings.ToValidUTF8(s, "�") }

// metadata keys carried on the ErrorInfo detail.
const (
	mdConfigPath = "configPath"
	mdSuggestion = "suggestion"
	mdDocsURL    = "docsUrl"
	mdFix        = "fix"
)

// ToStatus encodes a ConduitError as a gRPC *status.Status: the top-level code is
// the code's gRPC category and the message is the error message (unchanged from
// today's shape), plus an additive google.rpc.ErrorInfo detail carrying the stable
// reason and the structured fields. grpc-gateway preserves the detail into the
// HTTP JSON body via protojson, so HTTP consumers see it too.
func ToStatus(e *ConduitError) *status.Status {
	st := status.New(e.Code.GRPCCode(), valid(e.Message))

	md := map[string]string{}
	if e.ConfigPath != "" {
		md[mdConfigPath] = valid(e.ConfigPath)
	}
	if e.Suggestion != "" {
		md[mdSuggestion] = valid(e.Suggestion)
	}
	if e.DocsURL != "" {
		md[mdDocsURL] = valid(e.DocsURL)
	}
	if e.Fix != nil {
		fix := Fix{ConfigPath: valid(e.Fix.ConfigPath), Op: valid(e.Fix.Op), Value: valid(e.Fix.Value)}
		if b, err := json.Marshal(fix); err == nil {
			md[mdFix] = string(b)
		}
	}

	info := &errdetails.ErrorInfo{
		// Reason is sanitized like every other wire string: an invalid-UTF-8 byte
		// in any proto string field fails the whole detail's marshal, and ToStatus'
		// fallback would then silently drop all structured data. Registry reasons
		// are ASCII today, but a reason arriving from a non-Go plugin is not trusted.
		Reason:   valid(e.Code.Reason()),
		Domain:   errorDomain,
		Metadata: md,
	}

	withDetails, err := st.WithDetails(info)
	if err != nil {
		// WithDetails only fails if the detail can't be marshaled, which can't
		// happen for a well-formed ErrorInfo; fall back to the detail-less status.
		return st
	}
	return withDetails
}

// FromStatus reconstructs a ConduitError from a gRPC *status.Status. It reads the
// ErrorInfo detail if present; otherwise it synthesizes a code from the gRPC
// category so the result is always a well-formed ConduitError. It never panics on
// malformed input.
func FromStatus(st *status.Status) *ConduitError {
	if st == nil {
		return New(CodeUnknown, "")
	}

	for _, d := range st.Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != errorDomain {
			continue
		}

		// For a registered reason the LOCAL registry is authoritative for the gRPC
		// category (reasons are stable; a reason's category does not change across
		// versions). For an unknown reason we fall back to the wire status' code so
		// the error still round-trips. If a client and server ever disagree on a
		// registered reason's category, the local mapping wins by design.
		code, ok := LookupCode(info.GetReason())
		if !ok {
			code = Code{reason: info.GetReason(), grpcCode: st.Code()}
		}

		md := info.GetMetadata()
		ce := &ConduitError{
			Code:       code,
			Message:    st.Message(),
			ConfigPath: md[mdConfigPath],
			Suggestion: md[mdSuggestion],
			DocsURL:    md[mdDocsURL],
			err:        cerrors.New(st.Message()),
		}
		if raw, ok := md[mdFix]; ok && raw != "" {
			var fix Fix
			if err := json.Unmarshal([]byte(raw), &fix); err == nil {
				ce.Fix = &fix
			}
		}
		return ce
	}

	// No Conduit ErrorInfo detail: synthesize from the gRPC category.
	return &ConduitError{
		Code:    Code{reason: CodeUnknown.reason, grpcCode: st.Code()},
		Message: st.Message(),
		err:     cerrors.New(st.Message()),
	}
}
