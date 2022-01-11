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

package record

import (
	"encoding/json"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record/schema"
)

// Record ...
type Record struct {
	Position Position
	Metadata map[string]string

	// SourceID contains the source connector ID.
	SourceID string

	// CreatedAt represents the time when the change occurred in the source system.
	// If that's impossible to find out, then it should be the time the change was detected by Conduit.
	CreatedAt time.Time
	// ReadAt represents the time at which Conduit read the record.
	ReadAt time.Time

	// Key and payload are guaranteed to be non-nil, always.
	// However, they may be 'empty', i.e. not contain any real data.
	Key     Data
	Payload Data
}

// Position is a unique identifier for a record being process.
// It's a Source's responsibility to choose and assign record positions,
// as they will be used by the Source in subsequent pipeline runs.
type Position []byte

// String is used when displaying the position in logs.
func (p Position) String() string {
	if p != nil {
		return string(p)
	}
	return "<nil>"
}

// Data ...
type Data interface {
	Bytes() []byte
}

type StructuredData map[string]interface{}

func (c StructuredData) Bytes() []byte {
	b, err := json.Marshal(c)
	if err != nil {
		// Unlikely to happen, we receive content from a plugin through GRPC.
		// If the content could be marshaled as protobuf it can be as JSON.
		panic(cerrors.Errorf("StructuredData error while marshaling as JSON: %w", err))
	}
	return b
}

type RawData struct {
	Raw    []byte
	Schema schema.Schema
}

func (c RawData) Bytes() []byte {
	return c.Raw
}
