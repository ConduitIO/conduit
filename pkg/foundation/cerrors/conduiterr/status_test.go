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

package conduiterr_test

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRoundTrip_AllFields is the guard that keeps the Go and wire encodings from
// drifting: a fully-populated ConduitError survives ToStatus -> FromStatus intact.
func TestRoundTrip_AllFields(t *testing.T) {
	is := is.New(t)

	orig := conduiterr.New(conduiterr.CodeConnectorPluginNotFound, "connector plugin not found")
	orig.ConfigPath = "/connectors/1/plugin"
	orig.Suggestion = "run `conduit connectors install <name>`"
	orig.DocsURL = "https://conduit.io/docs/errors/connector.plugin_not_found"
	orig.Fix = &conduiterr.Fix{ConfigPath: "/connectors/1/plugin", Op: "set", Value: "builtin:postgres"}

	got := conduiterr.FromStatus(conduiterr.ToStatus(orig))

	is.Equal(got.Code.Reason(), orig.Code.Reason())
	is.Equal(got.Code.GRPCCode(), orig.Code.GRPCCode())
	is.Equal(got.Message, orig.Message)
	is.Equal(got.ConfigPath, orig.ConfigPath)
	is.Equal(got.Suggestion, orig.Suggestion)
	is.Equal(got.DocsURL, orig.DocsURL)
	is.Equal(got.Fix, orig.Fix)
}

// TestToStatus_TopLevelShapeUnchanged confirms the additive-compat claim: the
// top-level gRPC code and message are exactly the code's category and the message,
// with the structured data living only in an additive detail.
func TestToStatus_TopLevelShapeUnchanged(t *testing.T) {
	is := is.New(t)
	orig := conduiterr.New(conduiterr.CodeConnectorPluginNotFound, "not found")

	st := conduiterr.ToStatus(orig)

	is.Equal(st.Code(), codes.NotFound)
	is.Equal(st.Message(), "not found")
	is.Equal(len(st.Details()), 1) // exactly the additive ErrorInfo detail
}

// TestFromStatus_UnregisteredReason exercises the synthetic-code fallback: a
// reason not in the registry still reconstructs, carrying the wire reason and the
// status' gRPC category.
func TestFromStatus_UnregisteredReason(t *testing.T) {
	is := is.New(t)
	st := status.New(codes.FailedPrecondition, "custom")
	st, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   "some.unregistered_reason",
		Domain:   "conduit",
		Metadata: map[string]string{"configPath": "/x"},
	})
	is.NoErr(err)

	got := conduiterr.FromStatus(st)

	is.Equal(got.Code.Reason(), "some.unregistered_reason")
	is.Equal(got.Code.GRPCCode(), codes.FailedPrecondition)
	is.Equal(got.ConfigPath, "/x")
}

// TestFromStatus_NoDetail synthesizes a ConduitError from a plain status with no
// Conduit detail, without panicking.
func TestFromStatus_NoDetail(t *testing.T) {
	is := is.New(t)
	got := conduiterr.FromStatus(status.New(codes.NotFound, "plain not found"))

	is.Equal(got.Message, "plain not found")
	is.Equal(got.Code.GRPCCode(), codes.NotFound)
	is.True(got.Error() == "plain not found")
}

func TestFromStatus_Nil(t *testing.T) {
	is := is.New(t)
	got := conduiterr.FromStatus(nil)
	is.True(got != nil) // never returns nil
}

// Note on Code.Reason sanitization: ToStatus coerces Reason to valid UTF-8 like
// every other wire field. It is defensive (a Go caller could Register a code with a
// non-ASCII reason); it can't be exercised via a crafted *status.Status here because
// proto validates UTF-8 on marshal — WithDetails rejects an invalid-UTF-8 reason
// outright — which is also why the bad reason can't reach FromStatus over the wire.

// FuzzRoundTrip ensures ToStatus/FromStatus never panic on arbitrary codes and
// string field values, round-trip them faithfully, and are idempotent on a relay
// (a second round-trip must not lose data — the check that would catch a field
// that silently fails to marshal).
func FuzzRoundTrip(f *testing.F) {
	f.Add(uint8(0), "connector plugin not found", "/connectors/0/plugin", "install it", "https://x", "builtin:pg")
	f.Add(uint8(3), "", "", "", "", "")
	f.Add(uint8(1), "msg with \"quotes\" and \n newlines", "/a/b", "sug\tgest", "u", "v w")
	f.Add(uint8(2), "bad \x82 utf8", "/p\x82", "s\xff", "d\x80", "\x81val") // invalid-UTF-8 seed

	want := func(s string) string { return strings.ToValidUTF8(s, "�") }

	f.Fuzz(func(t *testing.T, codeSel uint8, msg, configPath, suggestion, docsURL, fixValue string) {
		all := conduiterr.Codes()
		code := all[int(codeSel)%len(all)]

		e := conduiterr.New(code, msg)
		e.ConfigPath = configPath
		e.Suggestion = suggestion
		e.DocsURL = docsURL
		if fixValue != "" {
			e.Fix = &conduiterr.Fix{ConfigPath: configPath, Op: "set", Value: fixValue}
		}

		got := conduiterr.FromStatus(conduiterr.ToStatus(e))

		if got.Code.Reason() != code.Reason() {
			t.Fatalf("code reason: got %q want %q", got.Code.Reason(), code.Reason())
		}
		if got.Code.GRPCCode() != code.GRPCCode() {
			t.Fatalf("code grpc: got %v want %v", got.Code.GRPCCode(), code.GRPCCode())
		}
		if got.Message != want(msg) {
			t.Fatalf("message: got %q want %q", got.Message, want(msg))
		}
		if got.ConfigPath != want(configPath) {
			t.Fatalf("configPath: got %q want %q", got.ConfigPath, want(configPath))
		}
		if got.Suggestion != want(suggestion) {
			t.Fatalf("suggestion: got %q want %q", got.Suggestion, want(suggestion))
		}
		if got.DocsURL != want(docsURL) {
			t.Fatalf("docsURL: got %q want %q", got.DocsURL, want(docsURL))
		}
		if fixValue != "" && (got.Fix == nil || got.Fix.Value != want(fixValue)) {
			t.Fatalf("fix: got %+v want value %q", got.Fix, want(fixValue))
		}

		// Relay: a second round-trip must be idempotent. If any field silently
		// failed to marshal (dropping the detail), the relay diverges.
		relay := conduiterr.FromStatus(conduiterr.ToStatus(got))
		if relay.Code.Reason() != got.Code.Reason() || relay.Message != got.Message ||
			relay.ConfigPath != got.ConfigPath || relay.Suggestion != got.Suggestion ||
			relay.DocsURL != got.DocsURL {
			t.Fatalf("relay not idempotent: %+v vs %+v", relay, got)
		}
	})
}
