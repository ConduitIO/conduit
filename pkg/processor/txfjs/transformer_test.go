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
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"reflect"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/record"
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
	tr, err := NewTransformer(`
logger.Debug("no entrypoint");
`, zerolog.Nop())

	if err == nil {
		t.Error("expected error if transformer has no entrypoint")
		return
	}
	assert.Equal(t, `failed creating JavaScript function: failed to get entrypoint function "transform"`, err.Error())
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
			name: "change incoming record",
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
			name: "return new record",
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

func TestTransformer_Filtering(t *testing.T) {
	testCases := []struct {
		name   string
		src    string
		input  record.Record
		filter bool
	}{
		{
			name: "always skip",
			src: `function transform(r) {
				return null;
			}`,
			input:  record.Record{},
			filter: false,
		},
		{
			name: "filter based on a field - positive",
			src: `function transform(r) {
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
			src: `function transform(r) {
				if (r.Metadata["keepme"] != undefined) {
					return r
				}
				return null;
			}`,
			input:  record.Record{Metadata: map[string]string{"foo": "bar"}},
			filter: false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			underTest, err := NewTransformer(tc.src, zerolog.New(zerolog.NewConsoleWriter()))
			assert.Ok(t, err)

			rec, err := underTest.Transform(tc.input)
			if tc.filter {
				assert.Ok(t, err)
				assert.Equal(t, tc.input, rec)
			} else {
				assert.True(t, reflect.ValueOf(rec).IsZero(), "expected zero value of record")
				assert.True(t, cerrors.Is(err, processor.ErrSkipRecord), "expected ErrSkipRecord")
			}
		})
	}
}
