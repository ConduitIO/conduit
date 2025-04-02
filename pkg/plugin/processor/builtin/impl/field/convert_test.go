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
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestConvertField_Process(t *testing.T) {
	proc := NewConvertProcessor(log.Nop())
	ctx := context.Background()
	var err error
	testCases := []struct {
		name   string
		field  string
		typ    string
		record opencdc.Record
		want   sdk.SingleRecord
	}{
		{
			name:  "string to int",
			field: ".Key.id",
			typ:   "int",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "54"},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": 54},
			},
		},
		{
			name:  "string to float",
			field: ".Key.id",
			typ:   "float",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "54"},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": 54.0},
			},
		},
		{
			name:  "string to bool",
			field: ".Key.id",
			typ:   "bool",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "1"},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": true},
			},
		},
		{
			name:  "string to string",
			field: ".Key.id",
			typ:   "string",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "54"},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": "54"},
			},
		},
		{
			name:  "int to int",
			field: ".Key.id",
			typ:   "int",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 54},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": 54},
			},
		},
		{
			name:  "int to float",
			field: ".Key.id",
			typ:   "float",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 54},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": 54.0},
			},
		},
		{
			name:  "int to bool",
			field: ".Key.id",
			typ:   "bool",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 1},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": true},
			},
		},
		{
			name:  "int to string",
			field: ".Key.id",
			typ:   "string",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 54},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": "54"},
			},
		},
		{
			name:  "float to int",
			field: ".Key.id",
			typ:   "int",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 54.0},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": 54},
			},
		},
		{
			name:  "float to float",
			field: ".Key.id",
			typ:   "float",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 54.0},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": 54.0},
			},
		},
		{
			name:  "float to bool",
			field: ".Key.id",
			typ:   "bool",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 1.0},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": true},
			},
		},
		{
			name:  "float to string",
			field: ".Key.id",
			typ:   "string",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 54.0},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": "54"},
			},
		},
		{
			name:  "bool to int",
			field: ".Key.id",
			typ:   "int",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": true},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": 1},
			},
		},
		{
			name:  "bool to float",
			field: ".Key.id",
			typ:   "float",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": false},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": 0.0},
			},
		},
		{
			name:  "bool to bool",
			field: ".Key.id",
			typ:   "bool",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": true},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": true},
			},
		},
		{
			name:  "bool to string",
			field: ".Key.id",
			typ:   "string",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": false},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": "false"},
			},
		},
		{
			name:  "bytes to string",
			field: ".Key.id",
			typ:   "string",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": []byte("foo")},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": "foo"},
			},
		},
		{
			name:  "int to time",
			field: ".Key.id",
			typ:   "time",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 1611254412345678999},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": time.Date(2021, 1, 21, 18, 40, 12, 345678999, time.UTC)},
			},
		},
		{
			name:  "string to time",
			field: ".Key.id",
			typ:   "time",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "2021-01-21T18:40:12.345678999Z"},
			},
			want: sdk.SingleRecord{
				Key: opencdc.StructuredData{"id": time.Date(2021, 1, 21, 18, 40, 12, 345678999, time.UTC)},
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			proc.config.Type = tc.typ
			proc.referenceResolver, err = sdk.NewReferenceResolver(tc.field)
			is.NoErr(err)
			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			is.Equal(output[0], tc.want)
		})
	}
}

func TestConvertField_ProcessFail(t *testing.T) {
	proc := NewConvertProcessor(log.Nop())
	ctx := context.Background()
	var err error
	testCases := []struct {
		name    string
		field   string
		typ     string
		record  opencdc.Record
		wantErr string
	}{
		{
			name:  "string to int, int out of range",
			field: ".Key.id",
			typ:   "int",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "9999999999999999999"},
			},
			wantErr: "value out of range",
		},
		{
			name:  "string to int, string is not a valid number",
			field: ".Key.id",
			typ:   "int",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "nan"},
			},
			wantErr: "invalid syntax",
		},
		{
			name:  "float to int, float is out of range",
			field: ".Key.id",
			typ:   "int",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": 9999999999999999999.0},
			},
			wantErr: "value out of range",
		},
		{
			name:  "string to time, invalid format",
			field: ".Key.id",
			typ:   "time",
			record: opencdc.Record{
				Key: opencdc.StructuredData{"id": "21.01.2021 18:40:12"},
			},
			wantErr: `parsing time "21.01.2021 18:40:12" as "2006-01-02T15:04:05.999999999Z07:00": cannot parse "21.01.2021 18:40:12" as "2006"`,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			proc.config.Type = tc.typ
			proc.referenceResolver, err = sdk.NewReferenceResolver(tc.field)
			is.NoErr(err)
			output := proc.Process(ctx, []opencdc.Record{tc.record})
			is.True(len(output) == 1)
			rec, ok := output[0].(sdk.ErrorRecord)
			is.True(ok)
			is.True(strings.Contains(rec.Error.Error(), tc.wantErr))
		})
	}
}

func TestConvertField_Configure(t *testing.T) {
	proc := NewConvertProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     config.Config{"field": ".Payload.After.foo", "type": "int"},
			wantErr: false,
		}, {
			name:    "invalid config, contains an invalid prefix for the field",
			cfg:     config.Config{"field": ".Metadata.foo", "type": "int"},
			wantErr: true,
		}, {
			name:    "invalid config, invalid prefix",
			cfg:     config.Config{"field": "aPayload.foo", "type": "int"},
			wantErr: true,
		}, {
			name:    "invalid config, invalid type",
			cfg:     config.Config{"field": ".Key.foo", "type": "map"},
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
			if tc.wantErr {
				is.True(err != nil)
				return
			}
			is.NoErr(err)
		})
	}
}
