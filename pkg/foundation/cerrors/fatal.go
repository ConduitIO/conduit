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

package cerrors

import "fmt"

// FatalError is an error type that will differentiate these from other errors that could be retried.
type FatalError struct {
	Err error
}

// NewFatalError creates a new FatalError.
func NewFatalError(err error) *FatalError {
	return &FatalError{Err: err}
}

// Unwrap returns the wrapped error.
func (f *FatalError) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.Err
}

// Error returns the error message.
func (f *FatalError) Error() string {
	if f.Err == nil {
		return ""
	}
	return fmt.Sprintf("fatal error: %v", f.Err)
}

// IsFatalError checks if the error is a FatalError.
func IsFatalError(err error) bool {
	_, ok := err.(*FatalError)
	return ok
}
