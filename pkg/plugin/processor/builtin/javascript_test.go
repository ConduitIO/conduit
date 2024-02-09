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
	"bytes"
	"context"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/dop251/goja"
	"github.com/matryer/is"
	"github.com/rs/zerolog"
	"testing"
)

func TestJSProcessor_Logger(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	var buf bytes.Buffer
	logger := log.New(zerolog.New(&buf))
	underTest := newJsProcessor(logger)
	err := underTest.Configure(
		ctx,
		map[string]string{
			"script": `
	function process(r) {
		logger.Info().Msg("Hello");
		return r
	}
	`,
		},
	)
	is.NoErr(err)

	err = underTest.Open(ctx)
	is.NoErr(err)

	_ = underTest.Process(context.Background(), []opencdc.Record{{}})

	is.Equal(`{"level":"info","message":"Hello"}`+"\n", buf.String()) // expected different log message
}

func TestJSProcessor_MissingEntrypoint(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	underTest := newJsProcessor(log.Nop())
	err := underTest.Configure(
		ctx,
		map[string]string{"script": `function something() { logger.Debug("no entrypoint"); }`},
	)
	is.NoErr(err)

	err = underTest.Open(ctx)
	is.True(err != nil) // expected error
	is.Equal(
		`failed initializing JS function: failed to get entrypoint function "process"`,
		err.Error(),
	) // expected different error message
}

func TestJSProcessor_Process(t *testing.T) {
	tests := []struct {
		name   string
		script string
		args   []opencdc.Record
		want   []sdk.ProcessedRecord
	}{
		{
			name: "change fields of structured record",
			script: `
				function process(rec) {
					rec.Position = "3";
					rec.Operation = "update";
					rec.Metadata["returned"] = "JS";
					rec.Key = RawData("baz");
					rec.Payload.After["ccc"] = "baz";
					return rec;
				}`,
			args: []opencdc.Record{
				{
					Position:  []byte("2"),
					Operation: opencdc.OperationCreate,
					Metadata:  opencdc.Metadata{"existing": "val"},
					Key:       opencdc.RawData("bar"),
					Payload: opencdc.Change{
						Before: nil,
						After: opencdc.StructuredData(
							map[string]interface{}{
								"aaa": 111,
								"bbb": []string{"foo", "bar"},
							},
						),
					},
				},
			},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Position:  []byte("3"),
					Operation: opencdc.OperationUpdate,
					Metadata:  opencdc.Metadata{"existing": "val", "returned": "JS"},
					Key:       opencdc.RawData("baz"),
					Payload: opencdc.Change{
						Before: nil,
						After: opencdc.StructuredData{
							"aaa": 111,
							"bbb": []string{"foo", "bar"},
							"ccc": "baz",
						},
					},
				},
			},
		},
		{
			name: "complete change incoming record with structured data",
			script: `
				function process(rec) {
					rec.Position = "3";
					rec.Metadata["returned"] = "JS";
					rec.Key = RawData("baz");
					rec.Payload.After = new StructuredData();
					rec.Payload.After["foo"] = "bar";
					return rec;
				}`,
			args: []opencdc.Record{
				{
					Position: []byte("2"),
					Metadata: opencdc.Metadata{"existing": "val"},
					Key:      opencdc.RawData("bar"),
					Payload: opencdc.Change{
						Before: nil,
						After:  opencdc.RawData("foo"),
					},
				},
			},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Position: []byte("3"),
					Metadata: opencdc.Metadata{"existing": "val", "returned": "JS"},
					Key:      opencdc.RawData("baz"),
					Payload: opencdc.Change{
						Before: nil,
						After: opencdc.StructuredData{
							"foo": "bar",
						},
					},
				},
			},
		},
		{
			name: "complete change incoming record with raw data",
			script: `
				function process(rec) {
					rec.Position = "3";
					rec.Metadata["returned"] = "JS";
					rec.Key = RawData("baz");
					rec.Payload.After = RawData(String.fromCharCode.apply(String, rec.Payload.After) + "bar");
					return rec;
				}`,
			args: []opencdc.Record{
				{
					Position: []byte("2"),
					Metadata: opencdc.Metadata{"existing": "val"},
					Key:      opencdc.RawData("bar"),
					Payload: opencdc.Change{
						Before: nil,
						After:  opencdc.RawData("foo"),
					},
				},
			},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Position: []byte("3"),
					Metadata: opencdc.Metadata{"existing": "val", "returned": "JS"},
					Key:      opencdc.RawData("baz"),
					Payload: opencdc.Change{
						Before: nil,
						After:  opencdc.RawData("foobar"),
					},
				},
			},
		},
		{
			name: "return new record with raw data",
			script: `
				function process(record) {
					r = new Record();
					r.Position = "3";
					r.Metadata["returned"] = "JS";
					r.Key = new RawData("baz");
					r.Payload.After = new RawData("foobar");
					return r;
				}`,
			args: []opencdc.Record{{Position: opencdc.Position("3")}},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Position: []byte("3"),
					Metadata: opencdc.Metadata{"returned": "JS"},
					Key:      opencdc.RawData("baz"),
					Payload: opencdc.Change{
						Before: nil,
						After:  opencdc.RawData("foobar"),
					},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			underTest := newTestJavaScriptProc(t, tc.script)

			got := underTest.Process(ctx, tc.args)
			is.Equal(tc.want, got) // expected different record
		})
	}
}

func TestJSProcessor_Filtering(t *testing.T) {
	testCases := []struct {
		name       string
		cfg        map[string]string
		input      []opencdc.Record
		skipRecord bool
	}{
		{
			name: "always skip",
			cfg: map[string]string{
				"script": `function process(r) {
				return null;
			}`,
			},
			input:      []opencdc.Record{{}},
			skipRecord: true,
		},
		{
			name: "filter based on a field - positive",
			cfg: map[string]string{
				"script": `function process(r) {
				if (r.Metadata["keepme"] != undefined) {
					return r
				}
				return null;
			}`,
			},
			input:      []opencdc.Record{{Metadata: opencdc.Metadata{"keepme": "yes"}}},
			skipRecord: false,
		},
		{
			name: "filter out based on a field - negative",
			cfg: map[string]string{
				"script": `function process(r) {
				if (r.Metadata["keepme"] != undefined) {
					return r
				}
				return null;
			}`,
			},
			input:      []opencdc.Record{{Metadata: opencdc.Metadata{"foo": "bar"}}},
			skipRecord: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			underTest := newJsProcessor(log.Test(t))
			err := underTest.Configure(ctx, tc.cfg)
			is.NoErr(err) // expected no error when configuration the JS processor
			err = underTest.Open(ctx)
			is.NoErr(err) // expected no error when opening the JS processor

			rec := underTest.Process(ctx, tc.input)
			_, ok := rec[0].(sdk.FilterRecord)
			is.Equal(tc.skipRecord, ok)
		})
	}
}

func TestJSProcessor_DataTypes(t *testing.T) {
	testCases := []struct {
		name  string
		src   string
		input []opencdc.Record
		want  []sdk.ProcessedRecord
	}{
		{
			name: "position from string",
			src: `function process(rec) {
				rec.Position = "foobar";
				return rec;
			}`,
			input: []opencdc.Record{{}},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Position: opencdc.Position("foobar"),
				},
			},
		},
		{
			name: "raw payload, data from string",
			src: `function process(rec) {
				rec.Payload.After = new RawData("foobar");
				return rec;
			}`,
			input: []opencdc.Record{{}},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Payload: opencdc.Change{
						Before: nil,
						After:  opencdc.RawData("foobar"),
					},
				},
			},
		},
		{
			name: "raw key, data from string",
			src: `function process(rec) {
				rec.Key = new RawData("foobar");
				return rec;
			}`,
			input: []opencdc.Record{{}},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Key: opencdc.RawData("foobar"),
				},
			},
		},
		{
			name: "update metadata",
			src: `function process(rec) {
				rec.Metadata["new_key"] = "new_value"
				delete rec.Metadata.remove_me;
				return rec;
			}`,
			input: []opencdc.Record{{
				Metadata: opencdc.Metadata{
					"old_key":   "old_value",
					"remove_me": "remove_me",
				},
			}},
			want: []sdk.ProcessedRecord{
				sdk.SingleRecord{
					Metadata: opencdc.Metadata{
						"old_key": "old_value",
						"new_key": "new_value",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			underTest := newTestJavaScriptProc(t, tc.src)

			got := underTest.Process(context.Background(), tc.input)
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
	underTest := newTestJavaScriptProc(t, src)

	r := []opencdc.Record{{
		Key: opencdc.RawData("test key"),
		Payload: opencdc.Change{
			Before: nil,
			After:  opencdc.RawData("test payload"),
		},
	}}
	got := underTest.Process(context.Background(), r)
	errRec, isErrRec := got[0].(sdk.ErrorRecord)
	is.True(isErrRec) // expected error
	target := &goja.Exception{}
	is.True(cerrors.As(errRec.Error, &target)) // expected a goja.Exception
}

func TestJSProcessor_BrokenJSCode(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	src := `function {`

	p := newJsProcessor(log.Test(t))
	err := p.Configure(
		ctx,
		map[string]string{
			"script": src,
		},
	)
	is.NoErr(err) // expected no error when configuration the JS processor

	err = p.Open(ctx)
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
		
		function process(rec) {
			rec.Metadata["updated_key"] = getValue()
			return rec;
		}
	`
	underTest := newTestJavaScriptProc(t, src)

	r := []opencdc.Record{{
		Metadata: opencdc.Metadata{
			"old_key": "old_value",
		},
	}}

	got := underTest.Process(context.Background(), r)
	rec, ok := got[0].(sdk.SingleRecord)
	is.True(ok) // expected a processed record
	is.Equal(
		sdk.SingleRecord{
			Metadata: opencdc.Metadata{
				"old_key":     "old_value",
				"updated_key": "updated_value",
			},
		},
		rec,
	) // expected different record
}

func newTestJavaScriptProc(t *testing.T, src string) *jsProcessor {
	is := is.New(t)
	ctx := context.Background()

	p := newJsProcessor(log.Test(t))
	err := p.Configure(
		ctx,
		map[string]string{
			"script": src,
		},
	)
	is.NoErr(err) // expected no error when configuration the JS processor
	err = p.Open(ctx)
	is.NoErr(err) // expected no error when opening the JS processor

	return p
}
