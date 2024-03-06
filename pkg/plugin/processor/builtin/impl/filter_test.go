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

package impl

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestFilter_Process(t *testing.T) {
	is := is.New(t)
	proc := NewFilterProcessor(log.Nop())
	records := []opencdc.Record{
		{
			Metadata: map[string]string{"key1": "val1"},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
		{
			Metadata: map[string]string{"key2": "val2"},
			Payload:  opencdc.Change{},
		},
	}
	want := []sdk.ProcessedRecord{sdk.FilterRecord{}, sdk.FilterRecord{}}
	output := proc.Process(context.Background(), records)
	is.Equal(output, want)
}
