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

package multierror

import "strings"

// Error is an error that contains multiple sub-errors.
type Error struct {
	errs []error
}

// Error formats all sub-error messages into a string separated with new lines.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}

	var sb strings.Builder
	for i, err := range e.errs {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(err.Error())
	}
	return sb.String()
}

// Errors returns all underlying errors.
func (e *Error) Errors() []error {
	return e.errs
}

// Append will combine all errors into a single error. Any nil errors will be
// skipped, only actual errors are retained. If only one error is supplied it
// will be returned directly. If no error is supplied the function will return
// nil. Otherwise an Error will be returned containing the errors as sub-errors.
func Append(err error, errs ...error) error {
	e1 := err
	for _, e2 := range errs {
		e1 = appendInternal(e1, e2)
	}
	return e1
}

func appendInternal(e1 error, e2 error) error {
	if e1 == nil {
		return e2
	}
	if e2 == nil {
		return e1
	}

	switch err := e1.(type) {
	case *Error:
		err.errs = append(err.errs, e2)
		return err
	default:
		return &Error{errs: []error{e1, e2}}
	}
}
