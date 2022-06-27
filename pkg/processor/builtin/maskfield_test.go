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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/conduitio/conduit/pkg/record/schema/mock"
	"github.com/google/go-cmp/cmp"
)

func TestMaskFieldKey_Build(t *testing.T) {
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
			Settings: map[string]string{maskFieldConfigField: ""},
		}},
		wantErr: true,
	}, {
		name: "non-empty field returns processor",
		args: args{config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
		}},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MaskFieldKey(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("MaskFieldKey() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestMaskFieldKey_Process(t *testing.T) {
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
		name: "structured data int",
		config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"foo": 0,
				"baz": nil,
			},
		},
		wantErr: false,
	}, {
		name: "structured data string",
		config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": "sensitive data",
				"baz": nil,
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"foo": "",
				"baz": nil,
			},
		},
		wantErr: false,
	}, {
		name: "structured data map",
		config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": map[string]interface{}{"bar": "buz"},
				"baz": nil,
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"foo": map[string]interface{}(nil),
				"baz": nil,
			},
		},
		wantErr: false,
	}, {
		name: "raw data without schema",
		config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
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
			Settings: map[string]string{maskFieldConfigField: "foo"},
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
			underTest, err := MaskFieldKey(tt.config)
			assert.Ok(t, err)
			got, err := underTest.Execute(context.Background(), tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("process() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("process() diff = %s", diff)
			}
		})
	}
}

func TestMaskFieldPayload_Build(t *testing.T) {
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
			Settings: map[string]string{maskFieldConfigField: ""},
		}},
		wantErr: true,
	}, {
		name: "non-empty field returns processor",
		args: args{config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
		}},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MaskFieldPayload(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("MaskFieldPayload() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestMaskFieldPayload_Process(t *testing.T) {
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
		name: "structured data int",
		config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"foo": 0,
				"baz": nil,
			},
		},
		wantErr: false,
	}, {
		name: "structured data string",
		config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": "sensitive data",
				"baz": nil,
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"foo": "",
				"baz": nil,
			},
		},
		wantErr: false,
	}, {
		name: "structured data map",
		config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": map[string]interface{}{"bar": "buz"},
				"baz": nil,
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"foo": map[string]interface{}(nil),
				"baz": nil,
			},
		},
		wantErr: false,
	}, {
		name: "raw data without schema",
		config: processor.Config{
			Settings: map[string]string{maskFieldConfigField: "foo"},
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
			Settings: map[string]string{maskFieldConfigField: "foo"},
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
			underTest, err := MaskFieldPayload(tt.config)
			assert.Ok(t, err)
			got, err := underTest.Execute(context.Background(), tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("process() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("process() diff = %s", diff)
			}
		})
	}
}
