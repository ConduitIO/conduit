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
	"runtime"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

var _, helperFilePath, _, _ = runtime.Caller(0)

func newError() error {
	return cerrors.New("foobar")
}

func outter() error {
	err := middle()
	if err != nil {
		return cerrors.Errorf("outter error caused by: %w", err)
	}
	return nil
}

func middle() error {
	err := newError()
	if err != nil {
		return cerrors.Errorf("middle error caused by: %w", err)
	}
	return nil
}
