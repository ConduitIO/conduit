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
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestSplitProcessor_Process_Success(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name   string
		field  string
		record opencdc.Record
		want   sdk.MultiRecord
	}{
		{
			name:  "single field",
			field: ".Key.myslice",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"myslice": []int{1, 2, 3}},
			},
			want: sdk.MultiRecord{
				opencdc.Record{
					Metadata: map[string]string{"split.index": "0"},
					Key:      opencdc.StructuredData{"myslice": 1},
				},
				opencdc.Record{
					Metadata: map[string]string{"split.index": "1"},
					Key:      opencdc.StructuredData{"myslice": 2},
				},
				opencdc.Record{
					Metadata: map[string]string{"split.index": "2"},
					Key:      opencdc.StructuredData{"myslice": 3},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			logger := log.Test(t)
			proc := NewSplitProcessor(logger)

			cfg := config.Config{"field": tc.field}
			err := proc.Configure(ctx, cfg)
			is.NoErr(err)

			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			is.Equal(output[0], tc.want)
		})
	}
}

func TestSplitProcessor_Process_Fail(t *testing.T) {
	ctx := context.Background()
	testCases := []struct {
		name    string
		field   string
		record  opencdc.Record
		wantErr string
	}{
		{
			name:  "not a slice",
			field: ".Key.id",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "123"},
			},
			wantErr: `field ".Key.id" is not a slice`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			logger := log.Test(t)
			proc := NewSplitProcessor(logger)

			cfg := config.Config{"field": tc.field}
			err := proc.Configure(ctx, cfg)
			is.NoErr(err)

			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			rec, ok := output[0].(sdk.ErrorRecord)
			is.True(ok)
			is.True(strings.Contains(rec.Error.Error(), tc.wantErr))
		})
	}
}

func TestSplitProcessor_Configure_Fail(t *testing.T) {
	proc := NewSplitProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name:    "invalid config, contains an invalid prefix for the field",
			cfg:     config.Config{"field": ".Metadata.foo"},
			wantErr: true,
		}, {
			name:    "invalid config, invalid prefix",
			cfg:     config.Config{"field": "aPayload.foo"},
			wantErr: true,
		}, {
			name:    "missing param",
			cfg:     config.Config{},
			wantErr: true,
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
