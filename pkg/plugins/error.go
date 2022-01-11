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

package plugins

import (
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

var (
	ErrNotRunning     = cerrors.New("plugin is not running")
	ErrAlreadyRunning = cerrors.New("plugin is already running")
)

// recoverableError represents an error that can be recovered from.
type recoverableError struct {
	err error
}

func (e *recoverableError) Error() string { return e.err.Error() }
func (e *recoverableError) Unwrap() error { return e.err }

// NewRecoverableError returns an error that can be recovered from. If a plugin
// returns this error, then Conduit will try to execute the action again with a
// backoff retry strategy.
func NewRecoverableError(err error) error {
	return &recoverableError{
		err: cerrors.Errorf("%w [recoverable]", err),
	}
}

// IsRecoverableError returns true if the error is recoverable, false otherwise.
func IsRecoverableError(err error) bool {
	var r *recoverableError
	return cerrors.As(err, &r)
}

func wrapRecoverableError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "[recoverable]") {
		// this error is recoverable, wrap it
		err = &recoverableError{err: err}
	}
	return err
}
