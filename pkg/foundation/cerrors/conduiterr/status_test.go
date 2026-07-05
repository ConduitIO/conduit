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

// FuzzRoundTrip ensures ToStatus/FromStatus never panic on arbitrary string field
// values and round-trip them faithfully.
func FuzzRoundTrip(f *testing.F) {
	f.Add("connector plugin not found", "/connectors/0/plugin", "install it", "https://x", "builtin:pg")
	f.Add("", "", "", "", "")
	f.Add("msg with \"quotes\" and \n newlines", "/a/b", "sug\tgest", "u", "v w")

	f.Fuzz(func(t *testing.T, msg, configPath, suggestion, docsURL, fixValue string) {
		e := conduiterr.New(conduiterr.CodeInvalidArgument, msg)
		e.ConfigPath = configPath
		e.Suggestion = suggestion
		e.DocsURL = docsURL
		if fixValue != "" {
			e.Fix = &conduiterr.Fix{ConfigPath: configPath, Op: "set", Value: fixValue}
		}

		got := conduiterr.FromStatus(conduiterr.ToStatus(e))

		// Wire string fields must be valid UTF-8; ToStatus coerces stray bytes to
		// U+FFFD rather than dropping the whole detail, so we compare against the
		// coerced form.
		want := func(s string) string { return strings.ToValidUTF8(s, "�") }

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
	})
}
