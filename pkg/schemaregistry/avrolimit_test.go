// Copyright © 2026 Meroxa, Inc.
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

package schemaregistry

import (
	"encoding/binary"
	"testing"

	"github.com/conduitio/conduit-commons/schema/avro"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/matryer/is"
)

func TestClassifyDecodeError(t *testing.T) {
	is := is.New(t)

	is.Equal(ClassifyDecodeError(nil), nil)

	other := cerrors.New("failed to get schema")
	is.Equal(ClassifyDecodeError(other), other) // unrelated errors pass through

	// The real codec error, produced by conduit-commons, not a hand-written
	// string: if a codec upgrade changes its wording, this test fails.
	srd, err := avro.Parse([]byte(`{"type":"record","name":"classify","fields":[{"name":"a","type":{"type":"array","items":"null"}}]}`))
	is.NoErr(err)
	payload := append(binary.AppendUvarint(nil, uint64(2*(DefaultAvroMaxElements+1))), 0)
	var v map[string]any
	decodeErr := srd.Unmarshal(payload, &v)
	is.True(decodeErr != nil)

	got := ClassifyDecodeError(cerrors.Errorf("failed decoding data: %w", decodeErr))
	ce, ok := conduiterr.Get(got)
	is.True(ok)
	is.Equal(ce.Code, CodeAvroLimitExceeded)
	is.Equal(ce.ConfigPath, AvroMaxElementsConfigPath)
	is.True(cerrors.Is(got, decodeErr)) // cause stays reachable

	// Errors that crossed the plugin protocol as strings are classified too.
	ce, ok = conduiterr.Get(ClassifyDecodeError(cerrors.New(decodeErr.Error())))
	is.True(ok)
	is.Equal(ce.Code, CodeAvroLimitExceeded)

	// Idempotent.
	is.Equal(ClassifyDecodeError(got), got)
}

func TestApplyAvroMaxElements_RejectsNegative(t *testing.T) {
	is := is.New(t)
	is.True(ApplyAvroMaxElements(-1) != nil)
}
