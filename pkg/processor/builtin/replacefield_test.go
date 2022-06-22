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
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/processor/transform"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/conduitio/conduit/pkg/record/schema/mock"
	"github.com/google/go-cmp/cmp"
)

func TestReplaceFieldKey_Build(t *testing.T) {
	type args struct {
		config transform.Config
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{{
		name:    "nil config returns error",
		args:    args{config: nil},
		wantErr: true,
	}, {
		name:    "empty config returns error",
		args:    args{config: map[string]string{}},
		wantErr: true,
	}, {
		name:    "empty exclude returns error",
		args:    args{config: map[string]string{replaceFieldConfigExclude: ""}},
		wantErr: true,
	}, {
		name:    "empty include returns error",
		args:    args{config: map[string]string{replaceFieldConfigInclude: ""}},
		wantErr: true,
	}, {
		name:    "empty rename returns error",
		args:    args{config: map[string]string{replaceFieldConfigRename: ""}},
		wantErr: true,
	}, {
		name:    "invalid rename returns error",
		args:    args{config: map[string]string{replaceFieldConfigRename: "foo,bar"}},
		wantErr: true,
	}, {
		name:    "non-empty exclude returns transform",
		args:    args{config: map[string]string{replaceFieldConfigExclude: "foo"}},
		wantErr: false,
	}, {
		name:    "non-empty include returns transform",
		args:    args{config: map[string]string{replaceFieldConfigInclude: "foo"}},
		wantErr: false,
	}, {
		name:    "valid rename returns transform",
		args:    args{config: map[string]string{replaceFieldConfigRename: "foo:c1,bar:c2"}},
		wantErr: false,
	}, {
		name: "non-empty all fields returns transform",
		args: args{config: map[string]string{
			replaceFieldConfigExclude: "foo",
			replaceFieldConfigInclude: "bar",
			replaceFieldConfigRename:  "foo:c1,bar:c2"},
		},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReplaceFieldKey(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReplaceFieldKey() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestReplaceFieldKey_Transform(t *testing.T) {
	type args struct {
		r record.Record
	}
	tests := []struct {
		name    string
		config  transform.Config
		args    args
		want    record.Record
		wantErr bool
	}{{
		name:   "structured data exclude",
		config: map[string]string{replaceFieldConfigExclude: "foo,bar"},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"baz": []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name:   "structured data include",
		config: map[string]string{replaceFieldConfigInclude: "foo,baz"},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"foo": 123,
				"baz": []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name:   "structured data rename",
		config: map[string]string{replaceFieldConfigRename: "foo:c1,bar:c2"},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"c1":  123,
				"c2":  1.2,
				"baz": []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name: "structured data exclude and rename",
		config: map[string]string{
			replaceFieldConfigExclude: "foo,baz",
			replaceFieldConfigRename:  "foo:c1,bar:c2",
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"c2": 1.2,
			},
		},
		wantErr: false,
	}, {
		name: "structured data include and rename",
		config: map[string]string{
			replaceFieldConfigInclude: "foo,baz",
			replaceFieldConfigRename:  "foo:c1,bar:c2",
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"c1":  123,
				"baz": []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name: "structured data exclude and include",
		config: map[string]string{
			replaceFieldConfigExclude: "foo,baz",
			replaceFieldConfigInclude: "baz,bar",
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo":   123,
				"bar":   1.2,
				"baz":   []byte("123"),
				"other": "something",
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"bar": 1.2,
			},
		},
		wantErr: false,
	}, {
		name: "structured data exclude, include and rename",
		config: map[string]string{
			replaceFieldConfigExclude: "foo,baz",
			replaceFieldConfigInclude: "baz,bar",
			replaceFieldConfigRename:  "foo:c1,bar:c2,other:asdf",
		},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"foo":   123,
				"bar":   1.2,
				"baz":   []byte("123"),
				"other": "something",
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"c2": 1.2,
			},
		},
		wantErr: false,
	}, {
		name:   "raw data without schema",
		config: map[string]string{replaceFieldConfigExclude: "foo"},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name:   "raw data with schema",
		config: map[string]string{replaceFieldConfigExclude: "foo"},
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
			txfFunc, err := ReplaceFieldKey(tt.config)
			assert.Ok(t, err)
			got, err := txfFunc(tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("Transform() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("Transform() diff = %s", diff)
			}
		})
	}
}

func TestReplaceFieldPayload_Build(t *testing.T) {
	type args struct {
		config transform.Config
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{{
		name:    "nil config returns error",
		args:    args{config: nil},
		wantErr: true,
	}, {
		name:    "empty config returns error",
		args:    args{config: map[string]string{}},
		wantErr: true,
	}, {
		name:    "empty exclude returns error",
		args:    args{config: map[string]string{replaceFieldConfigExclude: ""}},
		wantErr: true,
	}, {
		name:    "empty include returns error",
		args:    args{config: map[string]string{replaceFieldConfigInclude: ""}},
		wantErr: true,
	}, {
		name:    "empty rename returns error",
		args:    args{config: map[string]string{replaceFieldConfigRename: ""}},
		wantErr: true,
	}, {
		name:    "invalid rename returns error",
		args:    args{config: map[string]string{replaceFieldConfigRename: "foo,bar"}},
		wantErr: true,
	}, {
		name:    "non-empty exclude returns transform",
		args:    args{config: map[string]string{replaceFieldConfigExclude: "foo"}},
		wantErr: false,
	}, {
		name:    "non-empty include returns transform",
		args:    args{config: map[string]string{replaceFieldConfigInclude: "foo"}},
		wantErr: false,
	}, {
		name:    "valid rename returns transform",
		args:    args{config: map[string]string{replaceFieldConfigRename: "foo:c1,bar:c2"}},
		wantErr: false,
	}, {
		name: "non-empty all fields returns transform",
		args: args{config: map[string]string{
			replaceFieldConfigExclude: "foo",
			replaceFieldConfigInclude: "bar",
			replaceFieldConfigRename:  "foo:c1,bar:c2"},
		},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReplaceFieldPayload(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReplaceFieldPayload() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestReplaceFieldPayload_Transform(t *testing.T) {
	type args struct {
		r record.Record
	}
	tests := []struct {
		name    string
		config  transform.Config
		args    args
		want    record.Record
		wantErr bool
	}{{
		name:   "structured data exclude",
		config: map[string]string{replaceFieldConfigExclude: "foo,bar"},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"baz": []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name:   "structured data include",
		config: map[string]string{replaceFieldConfigInclude: "foo,baz"},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"foo": 123,
				"baz": []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name:   "structured data rename",
		config: map[string]string{replaceFieldConfigRename: "foo:c1,bar:c2"},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"c1":  123,
				"c2":  1.2,
				"baz": []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name: "structured data exclude and rename",
		config: map[string]string{
			replaceFieldConfigExclude: "foo,baz",
			replaceFieldConfigRename:  "foo:c1,bar:c2",
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"c2": 1.2,
			},
		},
		wantErr: false,
	}, {
		name: "structured data include and rename",
		config: map[string]string{
			replaceFieldConfigInclude: "foo,baz",
			replaceFieldConfigRename:  "foo:c1,bar:c2",
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo": 123,
				"bar": 1.2,
				"baz": []byte("123"),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"c1":  123,
				"baz": []byte("123"),
			},
		},
		wantErr: false,
	}, {
		name: "structured data exclude and include",
		config: map[string]string{
			replaceFieldConfigExclude: "foo,baz",
			replaceFieldConfigInclude: "baz,bar",
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo":   123,
				"bar":   1.2,
				"baz":   []byte("123"),
				"other": "something",
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"bar": 1.2,
			},
		},
		wantErr: false,
	}, {
		name: "structured data exclude, include and rename",
		config: map[string]string{
			replaceFieldConfigExclude: "foo,baz",
			replaceFieldConfigInclude: "baz,bar",
			replaceFieldConfigRename:  "foo:c1,bar:c2,other:asdf",
		},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"foo":   123,
				"bar":   1.2,
				"baz":   []byte("123"),
				"other": "something",
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"c2": 1.2,
			},
		},
		wantErr: false,
	}, {
		name:   "raw data without schema",
		config: map[string]string{replaceFieldConfigExclude: "foo"},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name:   "raw data with schema",
		config: map[string]string{replaceFieldConfigExclude: "foo"},
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
			txfFunc, err := ReplaceFieldPayload(tt.config)
			assert.Ok(t, err)
			got, err := txfFunc(tt.args.r)
			if (err != nil) != tt.wantErr {
				t.Errorf("Transform() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("Transform() diff = %s", diff)
			}
		})
	}
}
