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

package writer

import (
	"github.com/conduitio/conduit/pkg/plugins/s3/destination/format"
	"github.com/conduitio/conduit/pkg/record"
)

// Batch describes the data that needs to be saved by the Writer
type Batch struct {
	Format  format.Format
	Records []record.Record
}

// Bytes returns a byte representation for the Writer to write into a file.
func (b *Batch) Bytes() ([]byte, error) {
	return b.Format.MakeBytes(b.Records)
}

// LastPosition returns the position of the last record in the batch.
func (b *Batch) LastPosition() record.Position {
	if len(b.Records) == 0 {
		return nil
	}

	last := b.Records[len(b.Records)-1]
	return last.Position
}
