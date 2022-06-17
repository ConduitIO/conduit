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

package txfjs

import (
	"bytes"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/dop251/goja"
	"github.com/rs/zerolog"
)

func TestTransformer_Logger(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	tr, err := NewTransformer(`
	function transform(r) {
		logger.Info().Msg("Hello");
		return r
	}
	`, logger)
	assert.Ok(t, err)

	_, err = tr.Transform(record.Record{})
	assert.Ok(t, err)

	assert.Equal(t, `{"level":"info","message":"Hello"}`+"\n", buf.String())
}

func TestTransformer_Transform_MissingEntrypoint(t *testing.T) {
	tr, err := NewTransformer(
		`logger.Debug("no entrypoint");`,
		zerolog.Nop(),
	)

	if err == nil {
		t.Error("expected error if transformer has no entrypoint")
		return
	}
	assert.Equal(t, `failed to get entrypoint function "transform"`, err.Error())
	assert.True(t, tr == nil, "transformer should be nil")
}

func TestTransformer_Transform(t *testing.T) {
	type fields struct {
		src string
	}
	type args struct {
		record record.Record
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    record.Record
		wantErr error
	}{
		{
			// todo Once https://github.com/ConduitIO/conduit/issues/468 is implemented
			// write more tests which validate transforms on structured records
			name: "change non-payload fields of structured record",
			fields: fields{
				src: `
				function transform(record) {
					record.Position = "3";
					record.Metadata["returned"] = "JS";
					record.CreatedAt = new Date(Date.UTC(2021, 0, 2, 3, 4, 5, 6)).toISOString();
					record.Key.Raw = "baz";
					return record;
				}`,
			},
			args: args{
				record: record.Record{
					Position:  []byte("2"),
					Metadata:  map[string]string{"existing": "val"},
					CreatedAt: time.Now().UTC(),
					Key:       record.RawData{Raw: []byte("bar")},
					Payload: record.StructuredData(
						map[string]interface{}{
							"aaa": 111,
							"bbb": []string{"foo", "bar"},
						},
					),
				},
			},
			want: record.Record{
				Position:  []byte("3"),
				Metadata:  map[string]string{"existing": "val", "returned": "JS"},
				CreatedAt: time.Date(2021, time.January, 2, 3, 4, 5, 6000000, time.UTC),
				Key:       record.RawData{Raw: []byte("baz")},
				Payload: record.StructuredData(
					map[string]interface{}{
						"aaa": 111,
						"bbb": []string{"foo", "bar"},
					},
				),
			},
			wantErr: nil,
		},
		{
			name: "complete change incoming record with raw data",
			fields: fields{
				src: `
				function transform(record) {
					record.Position = "3";
					record.Metadata["returned"] = "JS";
					record.CreatedAt = new Date(Date.UTC(2021, 0, 2, 3, 4, 5, 6)).toISOString();
					record.Key.Raw = "baz";
					record.Payload.Raw = String.fromCharCode.apply(String, record.Payload.Raw) + "bar";
					return record;
				}`,
			},
			args: args{
				record: record.Record{
					Position:  []byte("2"),
					Metadata:  map[string]string{"existing": "val"},
					CreatedAt: time.Now().UTC(),
					Key:       record.RawData{Raw: []byte("bar")},
					Payload:   record.RawData{Raw: []byte("foo")},
				},
			},
			want: record.Record{
				Position:  []byte("3"),
				Metadata:  map[string]string{"existing": "val", "returned": "JS"},
				CreatedAt: time.Date(2021, time.January, 2, 3, 4, 5, 6000000, time.UTC),
				Key:       record.RawData{Raw: []byte("baz")},
				Payload:   record.RawData{Raw: []byte("foobar")},
			},
			wantErr: nil,
		},
		{
			name: "return new record with raw data",
			fields: fields{
				src: `
				function transform(record) {
					r = new Record();
					r.Position = "3";
					r.Metadata["returned"] = "JS";
					r.CreatedAt = new Date(Date.UTC(2021, 0, 2, 3, 4, 5, 6)).toISOString();
					r.Key = new RawData();
					r.Key.Raw = "baz";
					r.Payload = new RawData();
					r.Payload.Raw = "foobar"
					return r;
				}`,
			},
			args: args{
				record: record.Record{},
			},
			want: record.Record{
				Position:  []byte("3"),
				Metadata:  map[string]string{"returned": "JS"},
				CreatedAt: time.Date(2021, time.January, 2, 3, 4, 5, 6000000, time.UTC),
				Key:       record.RawData{Raw: []byte("baz")},
				Payload:   record.RawData{Raw: []byte("foobar")},
			},
			wantErr: nil,
		},
		{
			name: "no return value",
			fields: fields{
				src: `
				function transform() {
					logger.Debug("no return value");
				}`,
			},
			args: args{
				record: record.Record{},
			},
			want:    record.Record{},
			wantErr: cerrors.New("failed to transform to internal record: unexpected type, expected *record.Record, got <nil>"),
		},
		{
			name: "null return value",
			fields: fields{
				src: `
				function transform(record) {
					return null;
				}`,
			},
			args: args{
				record: record.Record{},
			},
			want:    record.Record{},
			wantErr: cerrors.New("failed to transform to internal record: unexpected type, expected *record.Record, got <nil>"),
		},
		{
			// todo do we want to allow this and similar transforms?
			name: "null key and null payload not allowed",
			fields: fields{
				src: `
				function transform(record) {
					record.Key = null;
					record.Payload = null;
					return record;
				}`,
			},
			args: args{
				record: record.Record{},
			},
			want:    record.Record{},
			wantErr: cerrors.New("failed to transform to internal record: unexpected type, expected *record.Record, got <nil>"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := NewTransformer(tt.fields.src, zerolog.Nop())
			assert.Ok(t, err)

			got, err := tr.Transform(tt.args.record)
			if err != nil {
				if tt.wantErr == nil || tt.wantErr.Error() != err.Error() {
					t.Errorf("wanted error: %+v - got error: %+v", tt.wantErr, err)
					return
				}
			}

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTransformer_DataTypes(t *testing.T) {
	testCases := []struct {
		name  string
		src   string
		input record.Record
		want  record.Record
	}{
		{
			name: "UTC date is used",
			src: `function transform(record) {
        		record.CreatedAt = new Date(Date.UTC(2021, 0, 2, 3, 4, 5, 6)).toISOString();
				return record;
			}`,
			input: record.Record{},
			want: record.Record{
				CreatedAt: time.Date(2021, time.January, 2, 3, 4, 5, 6000000, time.UTC),
			},
		},
		{
			name: "position from string",
			src: `function transform(record) {
        		record.Position = "foobar";
				return record;
			}`,
			input: record.Record{},
			want: record.Record{
				Position: record.Position("foobar"),
			},
		},
		{
			name: "raw payload, data from string",
			src: `function transform(record) {
				record.Payload = new RawData();
        		record.Payload.Raw = "foobar";
				return record;
			}`,
			input: record.Record{},
			want: record.Record{
				Payload: record.RawData{Raw: []byte("foobar")},
			},
		},
		{
			name: "raw key, data from string",
			src: `function transform(record) {
				record.Key = new RawData();
        		record.Key.Raw = "foobar";
				return record;
			}`,
			input: record.Record{},
			want: record.Record{
				Key: record.RawData{Raw: []byte("foobar")},
			},
		},
		{
			name: "update metadata",
			src: `function transform(record) {
				record.Metadata["new_key"] = "new_value"
				delete record.Metadata.remove_me;
				return record;
			}`,
			input: record.Record{
				Metadata: map[string]string{
					"old_key":   "old_value",
					"remove_me": "remove_me",
				},
			},
			want: record.Record{
				Metadata: map[string]string{
					"old_key": "old_value",
					"new_key": "new_value",
				},
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tr, err := NewTransformer(tc.src, zerolog.Nop())
			assert.Ok(t, err)

			got, err := tr.Transform(tc.input)
			assert.Ok(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTransformer_JavaScriptException(t *testing.T) {
	src := `function transform(record) {
		var m;
		m.test
	}`
	tr, err := NewTransformer(src, zerolog.Nop())
	assert.Ok(t, err)

	r := record.Record{
		Key:     record.RawData{Raw: []byte("test key")},
		Payload: record.RawData{Raw: []byte("test payload")},
	}

	got, err := tr.Transform(r)
	assert.Error(t, err)
	target := &goja.Exception{}
	assert.True(t, cerrors.As(err, &target), "expected a goja.Exception")
	assert.Equal(t, record.Record{}, got)
}

func TestTransformer_BrokenJSCode(t *testing.T) {
	src := `function {`
	_, err := NewTransformer(src, zerolog.Nop())
	assert.Error(t, err)
	target := &goja.CompilerSyntaxError{}
	assert.True(t, cerrors.As(err, &target), "expected a goja.CompilerSyntaxError")
}

func TestTransformer_ScriptWithMultipleFunctions(t *testing.T) {
	src := `
		function getValue() {
			return "updated_value";
		}
		
		function transform(record) {
			record.Metadata["updated_key"] = getValue()
			return record;
		}
	`
	tr, err := NewTransformer(src, zerolog.Nop())
	assert.Ok(t, err)

	r := record.Record{
		Metadata: map[string]string{
			"old_key": "old_value",
		},
	}

	got, err := tr.Transform(r)
	assert.Ok(t, err)
	assert.Equal(
		t,
		record.Record{
			Metadata: map[string]string{
				"old_key":     "old_value",
				"updated_key": "updated_value",
			},
		},
		got,
	)
}
