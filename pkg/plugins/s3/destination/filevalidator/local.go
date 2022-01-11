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

package filevalidator

import (
	"io/ioutil"
	"os"
	"path"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// Local validates files against local filesystem
type Local struct {
	Path string
}

// Validate takes a name of a local file and compares the contents of a local
// file with this name to a byte-slice returning an error if they don't match.
func (lfv *Local) Validate(name string, reference []byte) error {
	var err error

	filePath := path.Join(lfv.Path, name)

	fileBytes, err := ioutil.ReadFile(filePath)

	if err != nil {
		return err
	}

	err = compareBytes(fileBytes, reference)

	if err != nil {
		return cerrors.Errorf(
			"%s (%dB) and its reference (%dB) have different bytes: %w",
			name,
			len(fileBytes),
			len(reference),
			err,
		)
	}

	err = os.Remove(filePath)

	if err != nil {
		return cerrors.Errorf("could not remove %s: %w", filePath, err)
	}

	return nil
}
