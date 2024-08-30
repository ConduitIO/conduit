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

import (
	"fmt"
)

// fatalError is an error type that will differentiate these from other errors that could be retried.
type fatalError struct {
	Err error
}

// FatalError creates a new fatalError.
func FatalError(err error) *fatalError {
	return &fatalError{Err: err}
}

// Unwrap returns the wrapped error.
func (f *fatalError) Unwrap() error {
	return f.Err
}

// Error returns the error message.
func (f *fatalError) Error() string {
	if f.Err == nil {
		return ""
	}
	return fmt.Sprintf("fatal error: %v", f.Err)
}

// IsFatalError checks if the error is a fatalError.
func IsFatalError(err error) bool {
	var fatalErr *fatalError
	return As(err, &fatalErr)
}
