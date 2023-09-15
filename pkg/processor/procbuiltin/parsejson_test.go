// Copyright © 2023 Meroxa, Inc.
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
	"testing"

	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

func TestParseJSONKey_Process(t *testing.T) {
	tests := []struct {
		name    string
		record  record.Record
		want    record.Record
		wantErr bool
	}{{
		name: "raw key",
		record: record.Record{
			Key: record.RawData{
				Raw:    []byte("{\"after\":{\"data\":4,\"id\":3}}"),
				Schema: nil,
			},
		},
		want: record.Record{
			Key: record.StructuredData{
				"after": map[string]interface{}{"data": float64(4), "id": float64(3)},
			},
		},
		wantErr: false,
	}, {
		name: "already structured key",
		record: record.Record{
			Key: record.StructuredData{
				"after": map[string]interface{}{"data": float64(4), "id": float64(3)},
			},
		},
		want: record.Record{
			Key: record.StructuredData{
				"after": map[string]interface{}{"data": float64(4), "id": float64(3)},
			},
		},
		wantErr: false,
	}, {
		name: "invalid JSON key",
		record: record.Record{
			Key: record.RawData{
				Raw:    []byte("\"invalid\":\"json\""),
				Schema: nil,
			},
		},
		wantErr: true,
	},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			underTest, err := ParseJSONKey(processor.Config{})
			is.NoErr(err)
			got, err := underTest.Process(context.Background(), tt.record)
			if (err != nil) != tt.wantErr {
				t.Fatalf("process() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("process() diff = %s", diff)
			}
		})
	}
}

func TestParseJSONPayload_Process(t *testing.T) {
	tests := []struct {
		name    string
		record  record.Record
		want    record.Record
		wantErr bool
	}{{
		name: "raw payload",
		record: record.Record{
			Payload: record.Change{
				Before: record.RawData{
					Raw:    []byte("{\"ignored\":\"true\"}"),
					Schema: nil,
				},
				After: record.RawData{
					Raw:    []byte("{\"after\":{\"data\":4,\"id\":3}}"),
					Schema: nil,
				},
			}},
		want: record.Record{
			Payload: record.Change{
				Before: record.RawData{
					Raw:    []byte("{\"ignored\":\"true\"}"),
					Schema: nil,
				},
				After: record.StructuredData{
					"after": map[string]interface{}{"data": float64(4), "id": float64(3)},
				},
			},
		},
		wantErr: false,
	}, {
		name: "already structured payload",
		record: record.Record{
			Payload: record.Change{
				Before: nil,
				After: record.StructuredData{
					"after": map[string]interface{}{"data": float64(4), "id": float64(3)},
				},
			}},
		want: record.Record{
			Payload: record.Change{
				Before: nil,
				After: record.StructuredData{
					"after": map[string]interface{}{"data": float64(4), "id": float64(3)},
				},
			},
		},
		wantErr: false,
	}, {
		name: "nil after",
		record: record.Record{
			Payload: record.Change{
				Before: nil,
				After:  nil,
			},
		},
		want: record.Record{
			Payload: record.Change{
				Before: nil,
				After:  nil,
			},
		},
		wantErr: false,
	}, {
		name: "invalid JSON payload",
		record: record.Record{
			Payload: record.Change{
				After: record.RawData{
					Raw:    []byte("\"invalid\":\"true\""),
					Schema: nil,
				},
			}},
		wantErr: true,
	}, {
		name: "empty raw data parsed into empty structured data",
		record: record.Record{
			Payload: record.Change{
				Before: nil,
				After:  record.RawData{},
			},
		},
		want: record.Record{
			Payload: record.Change{
				Before: nil,
				After:  record.StructuredData(nil),
			},
		},
		wantErr: false,
	},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			underTest, err := ParseJSONPayload(processor.Config{})
			is.NoErr(err)
			got, err := underTest.Process(context.Background(), tt.record)
			if (err != nil) != tt.wantErr {
				t.Fatalf("process() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("process() diff = %s", diff)
			}
		})
	}
}
