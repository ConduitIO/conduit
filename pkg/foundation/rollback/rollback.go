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

import "github.com/conduitio/conduit/pkg/foundation/cerrors"

// R is a utility struct that can save rollback functions that need to be
// executed when something fails. It is meant to be declared as a value near the
// start of a function followed by a deferred call to R.Execute or
// R.MustExecute. The function can proceed normally and for every successful
// mutation it does it can register a function in R which rolls back that
// mutation. If the function succeeds it should call R.Skip before returning to
// skip the rollback.
//
// Example:
//
//	func foo() error {
//	  var r rollback.R
//	  defer r.MustExecute()
//
//	  // call action that mutates state and can fail
//	  err := mutableAction()
//	  if err != nil {
//	    return err
//	  }
//	  // append reversal of mutableAction1 to rollback
//	  r.Append(rollbackOfMutableAction)
//
//	  // do more mutable actions and append rollbacks to r
//	  // ...
//
//	  r.Skip() // all actions succeeded, skip rollbacks
//	  return nil
//	}
type R struct {
	f []func() error
}

// Append appends a function that can fail to R that will be called in Execute
// when executing a rollback.
func (r *R) Append(f func() error) {
	r.f = append(r.f, f)
}

// AppendPure appends a function that can't fail to R that will be called in
// Execute when executing a rollback.
func (r *R) AppendPure(f func()) {
	r.Append(func() error {
		f()
		return nil
	})
}

// Skip will remove all previously appended rollback functions from R. Any
// function that will be appended after the call to Skip will still be executed.
func (r *R) Skip() {
	r.f = nil
}

// Execute will run all appended functions in the reverse order (similar as a
// defer call). At the end it cleans up the appended functions, meaning that
// calling Execute a second time won't execute the functions anymore.
// If a rollback function returns an error Execute panics.
func (r *R) Execute() error {
	// execute rollbacks in reverse order
	for i := len(r.f) - 1; i >= 0; i-- {
		err := r.f[i]()
		if err != nil {
			// at this point we can't really do much, we probably have
			// data inconsistencies and a bug in the code
			return cerrors.Errorf("rollback failed: %w", err)
		}
		r.f = r.f[:i] // remove successful callback from slice
	}
	return nil
}

// MustExecute will call Execute and panic if it returns an error.
func (r *R) MustExecute() {
	err := r.Execute()
	if err != nil {
		panic(err)
	}
}
