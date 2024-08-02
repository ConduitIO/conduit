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

package field

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestSetField_Process(t *testing.T) {
	proc := NewSetProcessor(log.Nop())
	var err error
	ctx := context.Background()
	testCases := []struct {
		name   string
		config map[string]string
		record opencdc.Record
		want   sdk.SingleRecord
	}{
		{
			name:   "setting a metadata field",
			config: map[string]string{"field": ".Metadata.table", "value": "postgres"},
			record: opencdc.Record{
				Metadata: map[string]string{"table": "my-table"},
			},
			want: sdk.SingleRecord{
				Metadata: map[string]string{"table": "postgres"},
			},
		},
		{
			name:   "setting a non existent field",
			config: map[string]string{"field": ".Metadata.nonExistent", "value": "postgres"},
			record: opencdc.Record{
				Metadata: map[string]string{"table": "my-table"},
			},
			want: sdk.SingleRecord{
				Metadata: map[string]string{"table": "my-table", "nonExistent": "postgres"},
			},
		},
		{
			name:   "setting the operation field",
			config: map[string]string{"field": ".Operation", "value": "delete"},
			record: opencdc.Record{
				Operation: opencdc.OperationCreate,
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationDelete,
			},
		},
		{
			name:   "setting the payload.after with a go template evaluated value",
			config: map[string]string{"field": ".Payload.After.foo", "value": "{{ .Payload.After.baz }}"},
			record: opencdc.Record{
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"foo": "bar",
						"baz": "bar2",
					},
				},
			},
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"foo": "bar2",
						"baz": "bar2",
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err = proc.Configure(ctx, tc.config)
			is.NoErr(err)
			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			is.Equal(output[0], tc.want)
		})
	}
}

func TestSetField_Configure(t *testing.T) {
	proc := NewSetProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name    string
		cfg     map[string]string
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     map[string]string{"field": ".Metadata", "value": "{{ .Payload.After.foo }}"},
			wantErr: false,
		},
		{
			name:    "invalid value template format",
			cfg:     map[string]string{"field": ".Metadata", "value": "{{ invalid }}"},
			wantErr: true,
		},
		{
			name:    "value param is missing",
			cfg:     map[string]string{"field": ".Metadata"},
			wantErr: true,
		},
		{
			name:    "field param is missing",
			cfg:     map[string]string{"value": "sth"},
			wantErr: true,
		},
		{
			name:    "cannot set .Position",
			cfg:     map[string]string{"field": ".Position", "value": "newPos"},
			wantErr: true,
		},
		{
			name:    "all params are missing",
			cfg:     map[string]string{},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := proc.Configure(ctx, tc.cfg)
			if tc.wantErr {
				is.True(err != nil)
				return
			}
			is.NoErr(err)
		})
	}
}
