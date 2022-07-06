// Copyright © 2022 Meroxa, Inc.
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
	"runtime"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

var _, testFileLocation, _, _ = runtime.Caller(0)

type secretError struct {
}

func (s *secretError) Error() string {
	return "secret error message"
}

type unwrapPanicError struct {
}

func (w *unwrapPanicError) Error() string {
	return "calling Unwrap() will panic"
}

func (w *unwrapPanicError) Unwrap() error {
	panic("you didn't expect this to happen")
}

func TestNew(t *testing.T) {
	err := newError()
	s := fmt.Sprintf("%+v", err)
	assert.Equal(
		t,
		"foobar:\n    github.com/conduitio/conduit/pkg/foundation/cerrors_test.newError\n        "+helperFilePath+":26",
		s,
	)
}

func TestErrorf(t *testing.T) {
	err := cerrors.Errorf("caused by: %w", newError())
	s := fmt.Sprintf("%+v", err)
	assert.Equal(
		t,
		"caused by:\n    github.com/conduitio/conduit/pkg/foundation/cerrors_test.TestErrorf\n        "+
			testFileLocation+":58\n  - "+
			"foobar:\n    github.com/conduitio/conduit/pkg/foundation/cerrors_test.newError\n        "+
			helperFilePath+":26",
		s,
	)
}

func TestGetStackTrace(t *testing.T) {
	testCases := []struct {
		desc     string
		err      error
		expected []cerrors.Frame
	}{
		{
			desc:     "nil error",
			err:      nil,
			expected: nil,
		},
		{
			desc:     "third party error",
			err:      &secretError{},
			expected: nil,
		},
		{
			desc: "error wrapping third party error",
			err:  cerrors.Errorf("caused by: %w", &secretError{}),
			expected: []cerrors.Frame{
				{
					Func: "github.com/conduitio/conduit/pkg/foundation/cerrors_test.TestGetStackTrace",
					File: testFileLocation,
					Line: 88,
				},
			},
		},
		{
			desc:     "handle panics",
			err:      cerrors.Errorf("caused by: %w", &unwrapPanicError{}),
			expected: nil,
		},
		{
			desc: "single frame",
			err:  newError(),
			expected: []cerrors.Frame{
				{
					Func: "github.com/conduitio/conduit/pkg/foundation/cerrors_test.newError",
					File: helperFilePath,
					Line: 26,
				},
			},
		},
		{
			desc: "multiple frames",
			err:  outter(),
			expected: []cerrors.Frame{
				{
					Func: "github.com/conduitio/conduit/pkg/foundation/cerrors_test.outter",
					File: helperFilePath,
					Line: 32,
				},
				{
					Func: "github.com/conduitio/conduit/pkg/foundation/cerrors_test.middle",
					File: helperFilePath,
					Line: 40,
				},
				{
					Func: "github.com/conduitio/conduit/pkg/foundation/cerrors_test.newError",
					File: helperFilePath,
					Line: 26,
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			res := cerrors.GetStackTrace(tc.err)
			if tc.expected == nil {
				assert.Nil(t, res)
				return
			}
			act, ok := res.([]cerrors.Frame)
			assert.True(t, ok, "expected []cerrors.Frame")
			assert.Equal(t, tc.expected, act)
		})
	}
}

func TestLogOrReplace(t *testing.T) {
	is := is.New(t)

	errFoo := cerrors.New("foo")
	errBar := cerrors.New("bar")

	testCases := map[string]struct {
		oldErr        error
		newErr        error
		wantErr       error
		wantLogCalled bool
	}{
		"both nil": {
			oldErr:        nil,
			newErr:        nil,
			wantErr:       nil,
			wantLogCalled: false,
		},
		"oldErr exists, newErr nil": {
			oldErr:        errFoo,
			newErr:        nil,
			wantErr:       errFoo,
			wantLogCalled: false,
		},
		"oldErr nil, newErr exists": {
			oldErr:        nil,
			newErr:        errFoo,
			wantErr:       errFoo,
			wantLogCalled: false,
		},
		"both exist": {
			oldErr:        errFoo,
			newErr:        errBar,
			wantErr:       errFoo,
			wantLogCalled: true,
		}}

	for testName, tc := range testCases {
		t.Run(testName, func(t *testing.T) {
			logCalled := false
			gotErr := cerrors.LogOrReplace(tc.oldErr, tc.newErr, func() {
				logCalled = true
			})
			is.Equal(tc.wantErr, gotErr)
			is.Equal(tc.wantLogCalled, logCalled)
		})
	}
}
