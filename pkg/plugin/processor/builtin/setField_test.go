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

package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/matryer/is"
)

func TestSetField_Process(t *testing.T) {
	proc := setField{}
	ctx := context.Background()
	testCases := []struct {
		field  string
		value  string
		record opencdc.Record
		want   opencdc.Record
	}{
		{
			field: ".Metadata.table",
			value: "postgres",
			record: opencdc.Record{
				Metadata: map[string]string{"table": "my-table"},
			},
			want: opencdc.Record{
				Metadata: map[string]string{"table": "postgres"},
			},
		},
		{
			field: ".Operation",
			value: "delete",
			record: opencdc.Record{
				Operation: opencdc.OperationCreate,
			},
			want: opencdc.Record{
				Operation: opencdc.OperationDelete,
			},
		}, {
			field: ".Payload.After.foo",
			value: "new-val",
			record: opencdc.Record{
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"foo": "bar",
					},
				},
			},
			want: opencdc.Record{
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"foo": "new-val",
					},
				},
			},
		}}
	for _, tc := range testCases {
		t.Run(tc.field, func(t *testing.T) {
			is := is.New(t)
			proc.field = tc.field
			proc.value = tc.value
			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			is.Equal(output[0].(sdk.SingleRecord).Metadata, tc.want.Metadata)
			is.Equal(output[0].(sdk.SingleRecord).Payload, tc.want.Payload)
			is.Equal(output[0].(sdk.SingleRecord).Operation, tc.want.Operation)
		})
	}

}

func TestSetField_Configure(t *testing.T) {
	proc := setField{}
	ctx := context.Background()
	testCases := []struct {
		name    string
		cfg     map[string]string
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     map[string]string{"field": ".Metadata", "value": "sth"},
			wantErr: false,
		}, {
			name:    "value param is missing",
			cfg:     map[string]string{"field": ".Metadata"},
			wantErr: true,
		}, {
			name:    "field param is missing",
			cfg:     map[string]string{"value": "sth"},
			wantErr: true,
		}, {
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
				is.True(cerrors.Is(err, ErrRequiredParamMissing))
				return
			}
			is.NoErr(err)
		})
	}
}
