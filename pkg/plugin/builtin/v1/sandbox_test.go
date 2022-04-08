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

package builtinv1

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

func TestRunSandbox(t *testing.T) {
	is := is.New(t)
	haveErr := cerrors.New("test error")

	testData := []struct {
		name    string
		f       func() error
		wantErr error
		strict  bool
	}{{
		name:    "func returns nil",
		f:       func() error { return nil },
		wantErr: nil,
		strict:  true,
	}, {
		name:    "nil func",
		f:       nil,
		wantErr: cerrors.New("runtime error: invalid memory address or nil pointer dereference"),
		strict:  false,
	}, {
		name:    "func returns error",
		f:       func() error { return haveErr },
		wantErr: haveErr,
		strict:  true,
	}, {
		name:    "func returns error in panic",
		f:       func() error { panic(haveErr) },
		wantErr: haveErr,
		strict:  true,
	}, {
		name:    "func returns string in panic",
		f:       func() error { panic("oh noes something went bad") },
		wantErr: cerrors.New("panic: oh noes something went bad"),
		strict:  false,
	}, {
		name:    "func returns number in panic",
		f:       func() error { panic(1) },
		wantErr: cerrors.New("panic: 1"),
		strict:  false,
	}}

	for _, td := range testData {
		t.Run(td.name, func(t *testing.T) {
			gotErr := runSandbox(td.f)
			if td.strict {
				// strict mode means we expect a very specific error
				is.Equal(gotErr, td.wantErr)
			} else {
				// relaxed mode only compares error strings
				is.Equal(gotErr.Error(), td.wantErr.Error())
			}
		})
	}
}
