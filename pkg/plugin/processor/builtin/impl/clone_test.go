// Copyright © 2025 Meroxa, Inc.
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
	"strconv"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestCloneProcessor_Process_Success(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name   string
		count  int
		record opencdc.Record
		want   sdk.MultiRecord
	}{
		{
			name:  "with metadata",
			count: 1,
			record: opencdc.Record{
				Key:      opencdc.StructuredData{"myslice": []int{1, 2, 3}},
				Metadata: opencdc.Metadata{"foo": "bar"},
			},
			want: sdk.MultiRecord{
				opencdc.Record{
					Metadata: map[string]string{"clone.index": "0", "foo": "bar"},
					Key:      opencdc.StructuredData{"myslice": []int{1, 2, 3}},
				},
				opencdc.Record{
					Metadata: map[string]string{"clone.index": "1", "foo": "bar"},
					Key:      opencdc.StructuredData{"myslice": []int{1, 2, 3}},
				},
			},
		},
		{
			name:  "no metadata",
			count: 2,
			record: opencdc.Record{
				Key: opencdc.StructuredData{"myslice": []int{1, 2, 3}},
			},
			want: sdk.MultiRecord{
				opencdc.Record{
					Metadata: map[string]string{"clone.index": "0"},
					Key:      opencdc.StructuredData{"myslice": []int{1, 2, 3}},
				},
				opencdc.Record{
					Metadata: map[string]string{"clone.index": "1"},
					Key:      opencdc.StructuredData{"myslice": []int{1, 2, 3}},
				},
				opencdc.Record{
					Metadata: map[string]string{"clone.index": "2"},
					Key:      opencdc.StructuredData{"myslice": []int{1, 2, 3}},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			logger := log.Test(t)
			proc := NewCloneProcessor(logger)

			cfg := config.Config{"count": strconv.Itoa(tc.count)}
			err := proc.Configure(ctx, cfg)
			is.NoErr(err)

			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			is.Equal(output[0], tc.want)
		})
	}
}

func TestCloneProcessor_Configure_Fail(t *testing.T) {
	proc := NewCloneProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name string
		cfg  config.Config
	}{
		{
			name: "invalid config, less than 1",
			cfg:  config.Config{"count": "0"},
		}, {
			name: "invalid config, not a number",
			cfg:  config.Config{"count": "foo"},
		}, {
			name: "missing param",
			cfg:  config.Config{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := proc.Configure(ctx, tc.cfg)
			is.True(err != nil)
		})
	}
}
