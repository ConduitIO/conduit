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

package filterjs

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/rs/zerolog"
)

func TestFilter_Basic(t *testing.T) {
	underTest, err := NewFilter(
		`function filter(record) {
			return true;
		}`,
		false,
		zerolog.Nop(),
	)
	assert.Ok(t, err)
	r, err := underTest.Filter(record.Record{})
	assert.Equal(t, record.Record{}, r)
	assert.Equal(t, processor.ErrSkipRecord, err)
}

func TestFilter_Negate(t *testing.T) {
	underTest, err := NewFilter(
		`function filter(record) {
			return true;
		}`,
		true,
		zerolog.Nop(),
	)
	assert.Ok(t, err)
	r, err := underTest.Filter(record.Record{})
	assert.Equal(t, record.Record{}, r)
	assert.Ok(t, err)
}

func TestFilter_RecordShouldNotBeFiltered(t *testing.T) {
	underTest, err := NewFilter(
		`function filter(record) {
			return record.Metadata["filterme"] != undefined;
		}`,
		false,
		zerolog.Nop(),
	)
	assert.Ok(t, err)
	r, err := underTest.Filter(record.Record{})
	assert.Equal(t, record.Record{}, r)
	assert.Ok(t, err)
}

func TestFilter_RecordShouldBeFiltered(t *testing.T) {
	underTest, err := NewFilter(
		`function filter(record) {
			return record.Metadata["filterme"] != undefined;
		}`,
		false,
		zerolog.Nop(),
	)
	assert.Ok(t, err)
	r, err := underTest.Filter(record.Record{
		Metadata: map[string]string{
			"filterme": "yes of course",
		},
	})
	assert.Equal(t, record.Record{}, r)
	assert.Equal(t, processor.ErrSkipRecord, err)
}
