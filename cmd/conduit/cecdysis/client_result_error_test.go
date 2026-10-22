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

package cecdysis

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestNewClientResultError(t *testing.T) {
	coded := conduiterr.New(conduiterr.CodeInvalidArgument, "bad id")
	coded.Suggestion = "pass a pipeline ID"
	coded.ConfigPath = "/id"

	for name, tc := range map[string]struct {
		err            error
		wantCode       string
		wantMessage    string
		wantSuggestion string
		wantConfigPath string
	}{
		"coded status, wrapped by the CLI": {
			err:            cerrors.Errorf("failed to start pipeline %q: %w", "p", conduiterr.ToStatus(coded).Err()),
			wantCode:       "common.invalid_argument",
			wantMessage:    "bad id", // the server's message, not the wrapped text
			wantSuggestion: "pass a pipeline ID",
			wantConfigPath: "/id",
		},
		"status without a Conduit detail": {
			err:         grpcstatus.Error(codes.Internal, "boom"),
			wantCode:    conduiterr.CodeUnknown.Reason(),
			wantMessage: "boom",
		},
		"ConduitError raised in the CLI": {
			err:            coded,
			wantCode:       "common.invalid_argument",
			wantMessage:    "bad id",
			wantSuggestion: "pass a pipeline ID",
			wantConfigPath: "/id",
		},
		"plain error": {
			err:         cerrors.New("requires a pipeline ID"),
			wantCode:    conduiterr.CodeUnknown.Reason(),
			wantMessage: "requires a pipeline ID",
		},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			got := NewClientResultError(tc.err).Error
			is.Equal(got.Code, tc.wantCode)
			is.Equal(got.Message, tc.wantMessage)
			is.Equal(got.Suggestion, tc.wantSuggestion)
			is.Equal(got.ConfigPath, tc.wantConfigPath)
		})
	}
}
