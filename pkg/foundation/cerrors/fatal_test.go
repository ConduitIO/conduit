// Copyright © 2024 Meroxa, Inc.
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

package cerrors_test

import (
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

func TestNewFatalError(t *testing.T) {
	err := cerrors.New("test error")
	fatalErr := cerrors.NewFatalError(err)

	if fatalErr.Error() != fmt.Sprintf("fatal error: %v", err) {
		t.Errorf("expected error message to be %s, got %s", err.Error(), fatalErr.Error())
	}
}

func TestIsFatalError(t *testing.T) {
	err := cerrors.New("test error")

	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "FatalError",
			err:  cerrors.NewFatalError(err),
			want: true,
		},
		{
			name: "No Fatal Error",
			err:  cerrors.New("test error"),
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := cerrors.IsFatalError(tc.err)
			if got != tc.want {
				t.Errorf("IsFatalError(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestUnwrap(t *testing.T) {
	err := cerrors.New("test error")
	fatalErr := cerrors.NewFatalError(err)

	if cerrors.Unwrap(fatalErr) != err {
		t.Errorf("expected error to unwrap to %s, got %s", err.Error(), cerrors.Unwrap(fatalErr).Error())
	}
}

func TestFatalError(t *testing.T) {
	err := cerrors.New("test error")
	fatalErr := cerrors.NewFatalError(err)

	if fatalErr.Error() != fmt.Sprintf("fatal error: %v", err) {
		t.Errorf("expected error message to be %s, got %s", err.Error(), fatalErr.Error())
	}
}
