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

package connector

import (
	"testing"

	sdk "github.com/conduitio/conduit-connector-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
)

func TestMapSDKCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string // expected reason; empty means the error is returned unchanged
	}{
		{
			name: "code at the start of the message",
			err:  cerrors.New("sdk.batch.unbounded_wait: sdk.batch.size is 100 but sdk.batch.delay is 0"),
			want: sdk.CodeBatchUnboundedWait,
		},
		{
			name: "code wrapped by outer layers",
			err:  cerrors.Errorf("validation error: error configuring source: %w", cerrors.New("sdk.batch.size_defaulted: size defaulted to 1000")),
			want: sdk.CodeBatchSizeDefaulted,
		},
		{
			name: "code at the end of a sentence",
			err:  cerrors.New("rejected, see sdk.batch.drain_semantics_pending."),
			want: sdk.CodeBatchDrainSemanticsPending,
		},
		{
			name: "code only as a substring of a longer token",
			err:  cerrors.New("see docs for xsdk.batch.unbounded_wait.extra"),
		},
		{
			name: "unknown sdk code",
			err:  cerrors.New("sdk.batch.something_new: nope"),
		},
		{
			name: "no code (older SDK)",
			err:  cerrors.New("invalid config: sdk.batch.size must be >= 0"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got := MapSDKCode(tc.err)
			if tc.want == "" {
				is.Equal(got, tc.err) // unchanged
				return
			}
			ce, ok := conduiterr.Get(got)
			is.True(ok)
			is.Equal(ce.Code.Reason(), tc.want)
			is.Equal(ce.Code.GRPCCode(), codes.InvalidArgument)
			is.Equal(ce.Message, tc.err.Error()) // original message kept
			is.True(ce.ConfigPath != "")
			is.True(ce.Suggestion != "")
			is.True(cerrors.Is(got, tc.err)) // cause reachable
		})
	}

	t.Run("nil", func(t *testing.T) {
		is.New(t).NoErr(MapSDKCode(nil))
	})

	t.Run("does not shadow an inner code", func(t *testing.T) {
		is := is.New(t)
		inner := conduiterr.New(conduiterr.CodeUnknown, "sdk.batch.unbounded_wait: wrapped")
		is.Equal(MapSDKCode(inner), error(inner))
	})
}

// TestValidationError_CarriesSDKCode checks that the code survives the
// ValidationError wrapper the validation endpoint returns.
func TestValidationError_CarriesSDKCode(t *testing.T) {
	is := is.New(t)
	err := &ValidationError{Err: MapSDKCode(cerrors.New("sdk.batch.unbounded_wait: x"))}
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code.Reason(), sdk.CodeBatchUnboundedWait)
}

// TestSDKCodesMatchSDK keeps the registered reasons (string literals, see
// sdkcodes.go) equal to the constants the SDK exports.
func TestSDKCodesMatchSDK(t *testing.T) {
	is := is.New(t)
	is.Equal(CodeSDKBatchUnboundedWait.Reason(), sdk.CodeBatchUnboundedWait)
	is.Equal(CodeSDKBatchSizeDefaulted.Reason(), sdk.CodeBatchSizeDefaulted)
	is.Equal(CodeSDKBatchDrainSemanticsPending.Reason(), sdk.CodeBatchDrainSemanticsPending)
}
