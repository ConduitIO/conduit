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

package builtin

import (
	"context"
	"github.com/conduitio/conduit/pkg/processor"
	"reflect"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/conduitio/conduit/pkg/record/schema/mock"
)

func TestExtractFieldKey_Build(t *testing.T) {
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
		name: "empty field returns error",
		args: args{config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: ""},
		}},
		wantErr: true,
	}, {
		name: "non-empty field returns transform",
		args: args{config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
		}},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ExtractFieldKey(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractFieldKey() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestExtractFieldKey_Transform(t *testing.T) {
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
		name: "structured data",
		config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": 123,
			},
		}},
		want: record.Record{
			Key: record.RawData{
				Raw: []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name: "structured data field not found",
		config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"bar": 123,
				"baz": []byte("123"),
			},
		}},
		wantErr: true,
	}, {
		name: "raw data without schema",
		config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name: "raw data with schema",
		config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
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
			underTest, err := ExtractFieldKey(tt.config)
			assert.Ok(t, err)
			got, err := underTest.Execute(context.Background(), tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("Transform() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Transform() got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestExtractFieldPayload_Build(t *testing.T) {
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
		name: "empty field returns error",
		args: args{config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: ""},
		}},
		wantErr: true,
	}, {
		name: "non-empty field returns transform",
		args: args{config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
		}},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ExtractFieldPayload(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractFieldPayload() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestExtractFieldPayload_Transform(t *testing.T) {
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
		name: "structured data",
		config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": 123,
			},
		}},
		want: record.Record{
			Payload: record.RawData{
				Raw: []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name: "structured data field not found",
		config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"bar": 123,
				"baz": []byte("123"),
			},
		}},
		wantErr: true,
	}, {
		name: "raw data without schema",
		config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name: "raw data with schema",
		config: processor.Config{
			Settings: map[string]string{extractFieldConfigField: "foo"},
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
			underTest, err := ExtractFieldPayload(tt.config)
			assert.Ok(t, err)
			got, err := underTest.Execute(context.Background(), tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("Transform() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Transform() got = %v, want = %v", got, tt.want)
			}
		})
	}
}
