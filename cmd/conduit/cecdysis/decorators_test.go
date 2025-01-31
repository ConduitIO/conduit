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
	"testing"

	"github.com/matryer/is"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHandleError(t *testing.T) {
	is := is.New(t)

	testCases := []struct {
		name       string
		inputError error
		expected   error
	}{
		{
			name:       "OK error",
			inputError: status.Error(codes.OK, "ok error"),
			expected:   status.Error(codes.OK, "ok error"),
		},
		{
			name:       "Canceled error",
			inputError: status.Error(codes.Canceled, "canceled error"),
			expected:   status.Error(codes.Canceled, "canceled error"),
		},
		{
			name:       "Unknown error",
			inputError: status.Error(codes.Unknown, "unknown error"),
			expected:   status.Error(codes.Unknown, "unknown error"),
		},
		{
			name:       "InvalidArgument error",
			inputError: status.Error(codes.InvalidArgument, "invalid argument error"),
			expected:   status.Error(codes.InvalidArgument, "invalid argument error"),
		},
		{
			name:       "DeadlineExceeded error",
			inputError: status.Error(codes.DeadlineExceeded, "deadline exceeded error"),
			expected:   status.Error(codes.DeadlineExceeded, "deadline exceeded error"),
		},
		{
			name:       "NotFound error",
			inputError: status.Error(codes.NotFound, "not found error"),
			expected:   nil,
		},
		{
			name:       "AlreadyExists error",
			inputError: status.Error(codes.AlreadyExists, "already exists error"),
			expected:   status.Error(codes.AlreadyExists, "already exists error"),
		},
		{
			name:       "PermissionDenied error",
			inputError: status.Error(codes.PermissionDenied, "permission denied error"),
			expected:   status.Error(codes.PermissionDenied, "permission denied error"),
		},
		{
			name:       "ResourceExhausted error",
			inputError: status.Error(codes.ResourceExhausted, "resource exhausted error"),
			expected:   status.Error(codes.ResourceExhausted, "resource exhausted error"),
		},
		{
			name:       "FailedPrecondition error",
			inputError: status.Error(codes.FailedPrecondition, "failed precondition error"),
			expected:   status.Error(codes.FailedPrecondition, "failed precondition error"),
		},
		{
			name:       "Aborted error",
			inputError: status.Error(codes.Aborted, "aborted error"),
			expected:   status.Error(codes.Aborted, "aborted error"),
		},
		{
			name:       "OutOfRange error",
			inputError: status.Error(codes.OutOfRange, "out of range error"),
			expected:   status.Error(codes.OutOfRange, "out of range error"),
		},
		{
			name:       "Unimplemented error",
			inputError: status.Error(codes.Unimplemented, "unimplemented error"),
			expected:   status.Error(codes.Unimplemented, "unimplemented error"),
		},
		{
			name:       "Internal error",
			inputError: status.Error(codes.Internal, "internal error"),
			expected:   status.Error(codes.Internal, "internal error"),
		},
		{
			name:       "Unavailable error",
			inputError: status.Error(codes.Unavailable, "unavailable error"),
			expected:   status.Error(codes.Unavailable, "unavailable error"),
		},
		{
			name:       "DataLoss error",
			inputError: status.Error(codes.DataLoss, "data loss error"),
			expected:   status.Error(codes.DataLoss, "data loss error"),
		},
		{
			name:       "Unauthenticated error",
			inputError: status.Error(codes.Unauthenticated, "unauthenticated error"),
			expected:   status.Error(codes.Unauthenticated, "unauthenticated error"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			result := handleExecuteError(tc.inputError)
			is.Equal(result, tc.expected)
		})
	}
}
