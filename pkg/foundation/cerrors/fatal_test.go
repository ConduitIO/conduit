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
	"github.com/matryer/is"
)

func TestNewFatalError(t *testing.T) {
	is := is.New(t)

	err := cerrors.New("test error")
	fatalErr := cerrors.NewFatalError(err)
	wantErr := fmt.Sprintf("fatal error: %v", err)

	is.Equal(fatalErr.Error(), wantErr)
}

func TestIsFatalError(t *testing.T) {
	is := is.New(t)
	err := cerrors.New("test error")

	testCases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "when it's a FatalError",
			err:  cerrors.NewFatalError(err),
			want: true,
		},
		{
			name: "when it's wrapped in",
			err:  fmt.Errorf("something went wrong: %w", cerrors.NewFatalError(cerrors.New("fatal error"))),
			want: true,
		},
		{
			name: "when it's not a FatalError",
			err:  err,
			want: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := cerrors.IsFatalError(tc.err)
			is.Equal(got, tc.want)
		})
	}
}

func TestUnwrap(t *testing.T) {
	is := is.New(t)

	err := cerrors.New("test error")
	fatalErr := cerrors.NewFatalError(err)

	is.Equal(cerrors.Unwrap(fatalErr), err)
}

func TestFatalError(t *testing.T) {
	is := is.New(t)

	err := cerrors.New("test error")
	fatalErr := cerrors.NewFatalError(err)
	wantErr := fmt.Sprintf("fatal error: %v", err)

	is.Equal(fatalErr.Error(), wantErr)
}
