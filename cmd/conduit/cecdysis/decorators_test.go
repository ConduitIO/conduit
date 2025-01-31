// Copyright © 2025 Meroxa, Inc.
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
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHandleError(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		name           string
		inputError     error
		expected       error
		expectedStderr string
	}{
		{
			name:           "regular error",
			inputError:     cerrors.New("some error"),
			expectedStderr: "some error",
		},
		{
			name:           "Canceled error",
			inputError:     status.Error(codes.Canceled, "canceled error"),
			expectedStderr: "canceled error",
		},
		{
			name:           "Unknown error",
			inputError:     status.Error(codes.Unknown, "unknown error"),
			expectedStderr: "unknown error",
		},
		{
			name:           "InvalidArgument error",
			inputError:     status.Error(codes.InvalidArgument, "invalid argument error"),
			expectedStderr: "invalid argument error",
		},
		{
			name:           "DeadlineExceeded error",
			inputError:     status.Error(codes.DeadlineExceeded, "deadline exceeded error"),
			expectedStderr: "deadline exceeded error",
		},
		{
			name:           "NotFound error",
			inputError:     status.Error(codes.NotFound, "not found error"),
			expectedStderr: "not found error",
		},
		{
			name: "NotFound error with description",
			inputError: status.Error(codes.NotFound, "failed to get pipeline: rpc error: code = NotFound "+
				"desc = failed to get pipeline by ID: pipeline instance not found (ID: foo): pipeline instance not found"),
			expectedStderr: "failed to get pipeline by ID: pipeline instance not found (ID: foo): pipeline instance not found",
		},
		{
			name:           "AlreadyExists error",
			inputError:     status.Error(codes.AlreadyExists, "already exists error"),
			expectedStderr: "already exists error",
		},
		{
			name:           "PermissionDenied error",
			inputError:     status.Error(codes.PermissionDenied, "permission denied error"),
			expectedStderr: "permission denied error",
		},
		{
			name:           "ResourceExhausted error",
			inputError:     status.Error(codes.ResourceExhausted, "resource exhausted error"),
			expectedStderr: "resource exhausted error",
		},
		{
			name:           "FailedPrecondition error",
			inputError:     status.Error(codes.FailedPrecondition, "failed precondition error"),
			expectedStderr: "failed precondition error",
		},
		{
			name:           "Aborted error",
			inputError:     status.Error(codes.Aborted, "aborted error"),
			expectedStderr: "aborted error",
		},
		{
			name:           "OutOfRange error",
			inputError:     status.Error(codes.OutOfRange, "out of range error"),
			expectedStderr: "out of range error",
		},
		{
			name:           "Unimplemented error",
			inputError:     status.Error(codes.Unimplemented, "unimplemented error"),
			expectedStderr: "unimplemented error",
		},
		{
			name:           "Internal error",
			inputError:     status.Error(codes.Internal, "internal error"),
			expectedStderr: "internal error",
		},
		{
			name:           "Unavailable error",
			inputError:     status.Error(codes.Unavailable, "unavailable error"),
			expectedStderr: "unavailable error",
		},
		{
			name:           "DataLoss error",
			inputError:     status.Error(codes.DataLoss, "data loss error"),
			expectedStderr: "data loss error",
		},
		{
			name:           "Unauthenticated error",
			inputError:     status.Error(codes.Unauthenticated, "unauthenticated error"),
			expectedStderr: "unauthenticated error",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			result := handleError(tc.inputError)

			w.Close()
			os.Stderr = oldStderr

			var buf bytes.Buffer
			_, err := io.Copy(&buf, r)
			is.NoErr(err)

			is.Equal(result, tc.expected)
			is.Equal(strings.TrimSpace(buf.String()), tc.expectedStderr)
		})
	}
}
