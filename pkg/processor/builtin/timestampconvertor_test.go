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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/conduitio/conduit/pkg/record/schema/mock"
	"github.com/google/go-cmp/cmp"
)

func TestTimestampConvertorKey_Build(t *testing.T) {
	type args struct {
		config processor.Config
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
		name:    "empty field returns error",
		args:    args{config: map[string]string{timestampConvertorConfigField: ""}},
		wantErr: true,
	}, {
		name: "empty format returns error when targetType is string",
		args: args{config: map[string]string{timestampConvertorConfigField: "foo",
			timestampConvertorConfigTargetType: "string"}},
		wantErr: true,
	}, {
		name: "unix target type doesn't require a format",
		args: args{config: map[string]string{timestampConvertorConfigField: "foo",
			timestampConvertorConfigTargetType: "unix"}},
		wantErr: false,
	}, {
		name: "time.Time target type doesn't require a format, unless input type is string",
		args: args{config: map[string]string{timestampConvertorConfigField: "foo",
			timestampConvertorConfigTargetType: "time.Time",
			timestampConvertorConfigFormat:     "2016-01-02"}},
		wantErr: false,
	}, {
		name: "string targetType needs a format",
		args: args{config: map[string]string{timestampConvertorConfigField: "foo",
			timestampConvertorConfigTargetType: "string",
			timestampConvertorConfigFormat:     "2016-01-02"}},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TimestampConvertorKey(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("TimestampConvertorKey() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestTimestampConvertorKey_Transform(t *testing.T) {
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
		name: "from unix to string",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "string",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"date": int64(1621382400000000000),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"date": "2021-05-19",
			},
		},
		wantErr: false,
	}, {
		name: "from time.Time to string",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "string",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"date": time.Date(2021, time.May, 19, 0, 0, 0, 0, time.UTC),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"date": "2021-05-19",
			},
		},
		wantErr: false,
	}, {
		name: "from time.Time to unix",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "unix",
			timestampConvertorConfigFormat:     ""},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"date": time.Date(2021, time.May, 19, 0, 0, 0, 0, time.UTC),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"date": int64(1621382400000000000),
			},
		},
		wantErr: false,
	}, {
		name: "from string to unix",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "unix",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"date": "2021-05-19",
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"date": int64(1621382400000000000),
			},
		},
		wantErr: false,
	}, {
		name: "from string to time.Time",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "time.Time",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"date": "2021-05-19",
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"date": time.Date(2021, time.May, 19, 0, 0, 0, 0, time.UTC),
			},
		},
		wantErr: false,
	}, {
		name: "from string to time.Time with empty format should throw error",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "time.Time",
			timestampConvertorConfigFormat:     ""},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"date": "2021-05-19",
			},
		}},
		want:    record.Record{},
		wantErr: true,
	}, {
		name: "from string to unix with empty format should throw error",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "unix",
			timestampConvertorConfigFormat:     ""},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"date": "2021-05-19",
			},
		}},
		want:    record.Record{},
		wantErr: true,
	}, {
		name: "from unix to time.Time",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "time.Time",
			timestampConvertorConfigFormat:     ""},
		args: args{r: record.Record{
			Key: record.StructuredData{
				"date": int64(1621382400000000000),
			},
		}},
		want: record.Record{
			Key: record.StructuredData{
				"date": time.Date(2021, time.May, 19, 0, 0, 0, 0, time.UTC),
			},
		},
		wantErr: false,
	}, {
		name: "raw data without schema",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "string",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Key: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name:   "raw data with schema",
		config: map[string]string{timestampConvertorConfigField: "foo", timestampConvertorConfigTargetType: "unix"},
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
			txfFunc, err := TimestampConvertorKey(tt.config)
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

func TestTimestampConvertorPayload_Build(t *testing.T) {
	type args struct {
		config processor.Config
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
		name:    "empty field returns error",
		args:    args{config: map[string]string{timestampConvertorConfigField: ""}},
		wantErr: true,
	}, {
		name: "empty format returns error when targetType is string",
		args: args{config: map[string]string{timestampConvertorConfigField: "foo",
			timestampConvertorConfigTargetType: "string"}},
		wantErr: true,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := TimestampConvertorPayload(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("TimestampConvertorPayload() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestTimestampConvertorPayload_Transform(t *testing.T) {
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
		name: "from unix to string",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "string",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"date": int64(1621382400000000000),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"date": "2021-05-19",
			},
		},
		wantErr: false,
	}, {
		name: "from time.Time to string",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "string",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"date": time.Date(2021, time.May, 19, 0, 0, 0, 0, time.UTC),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"date": "2021-05-19",
			},
		},
		wantErr: false,
	}, {
		name: "from time.Time to unix",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "unix",
			timestampConvertorConfigFormat:     ""},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"date": time.Date(2021, time.May, 19, 0, 0, 0, 0, time.UTC),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"date": int64(1621382400000000000),
			},
		},
		wantErr: false,
	}, {
		name: "from string to unix",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "unix",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"date": "2021-05-19",
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"date": int64(1621382400000000000),
			},
		},
		wantErr: false,
	}, {
		name: "from string to time.Time",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "time.Time",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"date": "2021-05-19",
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"date": time.Date(2021, time.May, 19, 0, 0, 0, 0, time.UTC),
			},
		},
		wantErr: false,
	}, {
		name: "from string to time.Time with empty format should throw error",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "time.Time",
			timestampConvertorConfigFormat:     ""},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"date": "2021-05-19",
			},
		}},
		want:    record.Record{},
		wantErr: true,
	}, {
		name: "from string to unix with empty format should throw error",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "unix",
			timestampConvertorConfigFormat:     ""},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"date": "2021-05-19",
			},
		}},
		want:    record.Record{},
		wantErr: true,
	}, {
		name: "from unix to time.Time",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "time.Time",
			timestampConvertorConfigFormat:     ""},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"date": int64(1621382400000000000),
			},
		}},
		want: record.Record{
			Payload: record.StructuredData{
				"date": time.Date(2021, time.May, 19, 0, 0, 0, 0, time.UTC),
			},
		},
		wantErr: false,
	}, {
		name: "raw data without schema",
		config: map[string]string{timestampConvertorConfigField: "date",
			timestampConvertorConfigTargetType: "string",
			timestampConvertorConfigFormat:     "2006-01-02"},
		args: args{r: record.Record{
			Payload: record.RawData{
				Raw:    []byte("raw data"),
				Schema: nil,
			},
		}},
		wantErr: true, // not supported
	}, {
		name:   "raw data with schema",
		config: map[string]string{timestampConvertorConfigField: "foo", timestampConvertorConfigTargetType: "unix"},
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
			txfFunc, err := TimestampConvertorPayload(tt.config)
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
