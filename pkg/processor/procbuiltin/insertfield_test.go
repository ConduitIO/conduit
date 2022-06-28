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

package procbuiltin

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/conduitio/conduit/pkg/record/schema/mock"
)

func TestInsertFieldKey_Build(t *testing.T) {
	type args struct {
		config processor.Config
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{{
		name:    "nil config returns error",
		args:    args{config: processor.Config{}},
		wantErr: true,
	}, {
		name: "empty config returns error",
		args: args{config: processor.Config{
			Settings: map[string]string{},
		}},
		wantErr: true,
	}, {
		name: "static field without static value returns error",
		args: args{config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "",
			},
		}},
		wantErr: true,
	}, {
		name: "static field with empty static value returns error",
		args: args{config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "",
			},
		}},
		wantErr: true,
	}, {
		name: "static field with static value returns processor",
		args: args{config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "bar",
			},
		}},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := InsertFieldKey(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("InsertFieldKey() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestInsertFieldKey_Process(t *testing.T) {
	type args struct {
		r record.Record
	}
	tests := []struct {
		name    string
		config  processor.Config
		args    args
		want    record.Record
		wantErr bool
	}{{
		name: "static field in structured data",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "bar",
			},
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"bar": 123,
				"baz": nil,
				"foo": "bar",
			},
		},
		wantErr: false,
	}, {
		name: "static field in raw data without schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "bar",
			},
		},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name: "static field in raw data with schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "bar",
			},
		},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: mock.NewSchema(nil),
			},
		}},
		want:    record.Record{},
		wantErr: true, // TODO not implemented
	}, {
		name: "position in structured data",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigPositionField: "foo",
			},
		},
		args: args{r: record.Record{
			Position: record.Position("3"),
			Key: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Position: record.Position("3"),
			Key: record.StructuredData{
				"bar": 123,
				"baz": nil,
				"foo": record.Position("3"),
			},
		},
		wantErr: false,
	}, {
		name: "position in raw data without schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigPositionField: "foo",
			},
		},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name: "position in raw data with schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigPositionField: "foo",
			},
		},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: mock.NewSchema(nil),
			},
		}},
		want:    record.Record{},
		wantErr: true, // TODO not implemented
	}, {
		name: "timestamp in structured data",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigTimestampField: "foo",
			},
		},
		args: args{r: record.Record{
			CreatedAt: time.Unix(1234, 0),
			Key: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			CreatedAt: time.Unix(1234, 0),
			Key: record.StructuredData{
				"bar": 123,
				"baz": nil,
				"foo": time.Unix(1234, 0),
			},
		},
		wantErr: false,
	}, {
		name: "timestamp in raw data without schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigTimestampField: "foo",
			},
		},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name: "timestamp in raw data with schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigTimestampField: "foo",
			},
		},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: mock.NewSchema(nil),
			},
		}},
		want:    record.Record{},
		wantErr: true, // TODO not implemented
	}, {
		name: "all fields in structured data",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField:    "fooStatic",
				insertFieldConfigStaticValue:    "bar",
				insertFieldConfigPositionField:  "fooPosition",
				insertFieldConfigTimestampField: "fooTimestamp",
			},
		},
		args: args{r: record.Record{
			Position:  record.Position("321"),
			CreatedAt: time.Unix(321, 0),
			Key: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Position:  record.Position("321"),
			CreatedAt: time.Unix(321, 0),
			Key: record.StructuredData{
				"bar":          123,
				"baz":          nil,
				"fooStatic":    "bar",
				"fooPosition":  record.Position("321"),
				"fooTimestamp": time.Unix(321, 0),
			},
		},
		wantErr: false,
	}, {
		name: "all fields in raw data with schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField:    "fooStatic",
				insertFieldConfigStaticValue:    "bar",
				insertFieldConfigPositionField:  "fooPosition",
				insertFieldConfigTimestampField: "fooTimestamp",
			},
		},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: mock.NewSchema(nil),
			},
		}},
		want:    record.Record{},
		wantErr: true, // TODO not implemented
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			underTest, err := InsertFieldKey(tt.config)
			assert.Ok(t, err)
			got, err := underTest.Process(context.Background(), tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("process() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("process() got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestInsertFieldPayload_Build(t *testing.T) {
	type args struct {
		config processor.Config
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{{
		name:    "nil config returns error",
		args:    args{config: processor.Config{}},
		wantErr: true,
	}, {
		name: "empty config returns error",
		args: args{config: processor.Config{
			Settings: map[string]string{},
		}},
		wantErr: true,
	}, {
		name: "static field without static value returns error",
		args: args{config: processor.Config{
			Settings: map[string]string{insertFieldConfigStaticField: ""},
		}},
		wantErr: true,
	}, {
		name: "static field with empty static value returns error",
		args: args{config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "",
			},
		}},
		wantErr: true,
	}, {
		name: "static field with static value returns processor",
		args: args{config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "bar",
			},
		}},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := InsertFieldPayload(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("InsertFieldPayload() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestInsertFieldPayload_Process(t *testing.T) {
	type args struct {
		r record.Record
	}
	tests := []struct {
		name    string
		config  processor.Config
		args    args
		want    record.Record
		wantErr bool
	}{{
		name: "static field in structured data",
		config: processor.Config{
			Settings: map[string]string{insertFieldConfigStaticField: "foo", insertFieldConfigStaticValue: "bar"},
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"bar": 123,
				"baz": nil,
				"foo": "bar",
			},
		},
		wantErr: false,
	}, {
		name: "static field in raw data without schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "bar",
			},
		},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name: "static field in raw data with schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField: "foo",
				insertFieldConfigStaticValue: "bar",
			},
		},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: mock.NewSchema(nil),
			},
		}},
		want:    record.Record{},
		wantErr: true, // TODO not implemented
	}, {
		name: "position in structured data",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigPositionField: "foo",
			},
		},
		args: args{r: record.Record{
			Position: record.Position("3"),
			Payload: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Position: record.Position("3"),
			Payload: record.StructuredData{
				"bar": 123,
				"baz": nil,
				"foo": record.Position("3"),
			},
		},
		wantErr: false,
	}, {
		name: "position in raw data without schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigPositionField: "foo",
			},
		},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name: "position in raw data with schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigPositionField: "foo",
			},
		},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: mock.NewSchema(nil),
			},
		}},
		want:    record.Record{},
		wantErr: true, // TODO not implemented
	}, {
		name: "timestamp in structured data",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigTimestampField: "foo",
			},
		},
		args: args{r: record.Record{
			CreatedAt: time.Unix(1234, 0),
			Payload: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			CreatedAt: time.Unix(1234, 0),
			Payload: record.StructuredData{
				"bar": 123,
				"baz": nil,
				"foo": time.Unix(1234, 0),
			},
		},
		wantErr: false,
	}, {
		name: "timestamp in raw data without schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigTimestampField: "foo",
			},
		},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name: "timestamp in raw data with schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigTimestampField: "foo",
			},
		},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: mock.NewSchema(nil),
			},
		}},
		want:    record.Record{},
		wantErr: true, // TODO not implemented
	}, {
		name: "all fields in structured data",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField:    "fooStatic",
				insertFieldConfigStaticValue:    "bar",
				insertFieldConfigPositionField:  "fooPosition",
				insertFieldConfigTimestampField: "fooTimestamp",
			},
		},
		args: args{r: record.Record{
			Position:  record.Position("321"),
			CreatedAt: time.Unix(321, 0),
			Payload: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Position:  record.Position("321"),
			CreatedAt: time.Unix(321, 0),
			Payload: record.StructuredData{
				"bar":          123,
				"baz":          nil,
				"fooStatic":    "bar",
				"fooPosition":  record.Position("321"),
				"fooTimestamp": time.Unix(321, 0),
			},
		},
		wantErr: false,
	}, {
		name: "all fields in raw data with schema",
		config: processor.Config{
			Settings: map[string]string{
				insertFieldConfigStaticField:    "fooStatic",
				insertFieldConfigStaticValue:    "bar",
				insertFieldConfigPositionField:  "fooPosition",
				insertFieldConfigTimestampField: "fooTimestamp",
			},
		},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: mock.NewSchema(nil),
			},
		}},
		want:    record.Record{},
		wantErr: true, // TODO not implemented
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			underTest, err := InsertFieldPayload(tt.config)
			assert.Ok(t, err)
			got, err := underTest.Process(context.Background(), tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("process() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("process() got = %v, want = %v", got, tt.want)
			}
		})
	}
}
