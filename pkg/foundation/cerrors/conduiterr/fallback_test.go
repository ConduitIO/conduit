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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
)

func TestWithUnknownReason(t *testing.T) {
	is := is.New(t)

	cause := cerrors.New("some legacy sentinel")
	ce := WithUnknownReason(cause, codes.NotFound)

	is.Equal(ce.Code.Reason(), CodeUnknown.Reason()) // reason is CodeUnknown's, not fabricated
	is.Equal(ce.Code.GRPCCode(), codes.NotFound)     // category is the caller-supplied one, not CodeUnknown's Internal
	is.True(cerrors.Is(ce, cause))                   // the cause stays reachable through the chain
	is.Equal(ce.Error(), cause.Error())              // message unchanged
}

// TestWithUnknownReason_PreservesExistingCode guards the fallback against
// downgrading an error that already carries a real code (the silent-downgrade
// this whole guard exists to prevent).
func TestWithUnknownReason_PreservesExistingCode(t *testing.T) {
	is := is.New(t)

	coded := New(CodeInvalidArgument, "bad field")
	got := WithUnknownReason(coded, codes.NotFound)

	is.Equal(got.Code.Reason(), CodeInvalidArgument.Reason()) // real reason preserved, not internal.unknown
	is.Equal(got.Code.GRPCCode(), codes.InvalidArgument)      // real category preserved, not the NotFound arg
}
