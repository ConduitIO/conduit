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

package rollback

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

type callRecorder struct {
	returnError bool
	calls       int
}

func (cr *callRecorder) f() error {
	cr.calls++
	if cr.returnError {
		return cerrors.New("test error")
	}
	return nil
}

func TestRollback_ExecuteEmpty(t *testing.T) {
	is := is.New(t)

	var r R
	err := r.Execute()
	is.NoErr(err)
}

func TestRollback_ExecuteTwice(t *testing.T) {
	is := is.New(t)

	var r R
	var cr callRecorder

	r.Append(cr.f)
	err := r.Execute()

	is.NoErr(err)
	is.Equal(1, cr.calls)

	err = r.Execute()
	is.NoErr(err)
	is.Equal(1, cr.calls) // still only 1 call
}

func TestRollback_ExecuteMany(t *testing.T) {
	is := is.New(t)

	var r R
	var cr callRecorder
	const wantCalls = 100

	for i := 0; i < wantCalls; i++ {
		r.Append(cr.f)
	}
	err := r.Execute()

	is.NoErr(err)
	is.Equal(wantCalls, cr.calls)
}

func TestRollback_ExecuteError(t *testing.T) {
	is := is.New(t)

	var r R
	var cr callRecorder
	cr.returnError = true // rollback will return an error

	r.Append(cr.f)
	err := r.Execute()

	is.True(err != nil)
	is.Equal(1, cr.calls)

	// calling Execute again should try the same rollback again
	cr.returnError = false // let's succeed this time
	err = r.Execute()

	is.NoErr(err)
	is.Equal(2, cr.calls)
}

func TestRollback_MustExecuteSuccess(t *testing.T) {
	is := is.New(t)

	var r R
	var cr callRecorder

	defer func() {
		if recover() != nil {
			t.Fatal("Execute should not have panicked")
		}
		is.Equal(1, cr.calls)
	}()

	r.Append(cr.f)
	defer r.MustExecute()
}

func TestRollback_MustExecutePanic(t *testing.T) {
	is := is.New(t)

	var r R
	var cr callRecorder
	cr.returnError = true // rollback will return an error

	defer func() {
		if recover() == nil {
			t.Fatal("Execute should have panicked")
		}
		is.Equal(1, cr.calls)
	}()

	r.Append(cr.f)
	defer r.MustExecute()
}

func TestRollback_ExecutePure(t *testing.T) {
	is := is.New(t)

	var r R
	var called bool
	r.AppendPure(func() {
		called = true
	})
	err := r.Execute()
	is.NoErr(err)
	is.True(called) // rollback func was not called
}

func TestRollback_Skip(t *testing.T) {
	is := is.New(t)

	var r R
	var cr callRecorder

	r.Append(cr.f)
	r.Skip()           // skip should remove all rollback calls
	err := r.Execute() // execute does nothing

	is.NoErr(err)
	is.Equal(0, cr.calls)
}
