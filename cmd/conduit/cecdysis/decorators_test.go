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
			name:       "NotFound error",
			inputError: status.Error(codes.NotFound, "not found error"),
			expected:   nil,
		},
		{
			name:       "Internal error",
			inputError: status.Error(codes.Internal, "internal error"),
			expected:   status.Error(codes.Internal, "internal error"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			result := handleError(tc.inputError)
			is.Equal(result, tc.expected)
		})
	}
}
