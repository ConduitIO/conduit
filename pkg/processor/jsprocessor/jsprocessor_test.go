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

package jsprocessor

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/dop251/goja"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
)

func TestJSProcessor_Logger(t *testing.T) {
	is := is.New(t)

	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	underTest, err := New(`
	function process(r) {
		logger.Info().Msg("Hello");
		return r
	}
	`, logger)
	is.NoErr(err) // expected no error when creating the JS processor

	_, err = underTest.Execute(context.Background(), record.Record{})
	is.NoErr(err) // expected no error when processing record

	is.Equal(`{"level":"info","message":"Hello"}`+"\n", buf.String()) // expected different log message
}

func TestJSProcessor_MissingEntrypoint(t *testing.T) {
	is := is.New(t)

	underTest, err := New(
		`logger.Debug("no entrypoint");`,
		zerolog.Nop(),
	)

	is.True(err != nil)                                                                                   // expected error
	is.Equal(`failed initializing JS function: failed to get entrypoint function "process"`, err.Error()) // expected different error message
	is.True(underTest == nil)
}

func TestJSProcessor_Process(t *testing.T) {
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
			// write more tests which validate processors on structured records
			name: "change non-payload fields of structured record",
			fields: fields{
				src: `
				function process(record) {
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
				function process(record) {
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
				function process(record) {
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			underTest, err := New(tt.fields.src, zerolog.Nop())
			is.NoErr(err) // expected no error when creating the JS processor

			got, err := underTest.Execute(context.Background(), tt.args.record)
			if tt.wantErr != nil {
				is.Equal(tt.wantErr, err) // expected different error
			} else {
				is.NoErr(err) // expected no error
			}

			is.Equal(tt.want, got) // expected different record
		})
	}
}

func TestJSProcessor_Filtering(t *testing.T) {
	testCases := []struct {
		name   string
		src    string
		input  record.Record
		filter bool
	}{
		{
			name: "always skip",
			src: `function process(r) {
				return null;
			}`,
			input:  record.Record{},
			filter: false,
		},
		{
			name: "filter based on a field - positive",
			src: `function process(r) {
				if (r.Metadata["keepme"] != undefined) {
					return r
				}
				return null;
			}`,
			input:  record.Record{Metadata: map[string]string{"keepme": "yes"}},
			filter: true,
		},
		{
			name: "filter out based on a field - negative",
			src: `function process(r) {
				if (r.Metadata["keepme"] != undefined) {
					return r
				}
				return null;
			}`,
			input:  record.Record{Metadata: map[string]string{"foo": "bar"}},
			filter: false,
		},
		{
			name: "no return value",
			src: `
				function process(record) {
					logger.Debug("no return value");
				}`,
			input:  record.Record{Metadata: map[string]string{"foo": "bar"}},
			filter: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest, err := New(tc.src, zerolog.New(zerolog.NewConsoleWriter()))
			is.NoErr(err) // expected no error when creating the JS processor

			rec, err := underTest.Execute(context.Background(), tc.input)
			if tc.filter {
				is.NoErr(err)           // expected no error for processed record
				is.Equal(tc.input, rec) // expected different processed record
			} else {
				is.True(reflect.ValueOf(rec).IsZero())            // expected zero record
				is.True(cerrors.Is(err, processor.ErrSkipRecord)) // expected ErrSkipRecord
			}
		})
	}
}

func TestJSProcessor_DataTypes(t *testing.T) {
	testCases := []struct {
		name  string
		src   string
		input record.Record
		want  record.Record
	}{
		{
			name: "UTC date is used",
			src: `function process(record) {
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
			src: `function process(record) {
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
			src: `function process(record) {
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
			src: `function process(record) {
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
			src: `function process(record) {
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
			is := is.New(t)

			underTest, err := New(tc.src, zerolog.Nop())
			is.NoErr(err) // expected no error when creating the JS processor

			got, err := underTest.Execute(context.Background(), tc.input)
			is.NoErr(err)          // expected no error when processing record
			is.Equal(tc.want, got) // expected different record
		})
	}
}

func TestJSProcessor_JavaScriptException(t *testing.T) {
	is := is.New(t)

	src := `function process(record) {
		var m;
		m.test
	}`
	underTest, err := New(src, zerolog.Nop())
	is.NoErr(err) // expected no error when creating the JS processor

	r := record.Record{
		Key:     record.RawData{Raw: []byte("test key")},
		Payload: record.RawData{Raw: []byte("test payload")},
	}

	got, err := underTest.Execute(context.Background(), r)
	is.True(err != nil) // expected error
	target := &goja.Exception{}
	is.True(cerrors.As(err, &target)) // expected a goja.Exception
	is.Equal(record.Record{}, got)    // expected a zero record
}

func TestJSProcessor_BrokenJSCode(t *testing.T) {
	is := is.New(t)

	src := `function {`
	_, err := New(src, zerolog.Nop())
	is.True(err != nil) // expected error for invalid JS code
	target := &goja.CompilerSyntaxError{}
	is.True(cerrors.As(err, &target)) // expected a goja.CompilerSyntaxError
}

func TestJSProcessor_ScriptWithMultipleFunctions(t *testing.T) {
	is := is.New(t)

	src := `
		function getValue() {
			return "updated_value";
		}
		
		function process(record) {
			record.Metadata["updated_key"] = getValue()
			return record;
		}
	`
	underTest, err := New(src, zerolog.Nop())
	is.NoErr(err) // expected no error when creating the JS processor

	r := record.Record{
		Metadata: map[string]string{
			"old_key": "old_value",
		},
	}

	got, err := underTest.Execute(context.Background(), r)
	is.NoErr(err) // expected no error when processing record
	is.Equal(
		record.Record{
			Metadata: map[string]string{
				"old_key":     "old_value",
				"updated_key": "updated_value",
			},
		},
		got,
	) // expected different record
}
