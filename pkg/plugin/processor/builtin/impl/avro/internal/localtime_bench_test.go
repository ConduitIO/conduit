// Copyright © 2026 Meroxa, Inc.
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

package internal

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit-commons/schema"
)

// benchSchema returns a 30-field record schema, a third of whose fields have
// the given timestamp logical type, and a row for it.
func benchSchema(logical string) (schema.Schema, opencdc.StructuredData) {
	var f []string
	sd := opencdc.StructuredData{}
	for i := range 30 {
		name := fmt.Sprintf("c%02d", i)
		switch i % 3 {
		case 0:
			f = append(f, `{"name":"`+name+`","type":"int"}`)
			sd[name] = i
		case 1:
			f = append(f, `{"name":"`+name+`","type":["null","string"]}`)
			sd[name] = "v"
		default:
			f = append(f, `{"name":"`+name+`","type":{"type":"long","logicalType":"`+logical+`"}}`)
			sd[name] = time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
		}
	}
	return schema.Schema{Type: schema.TypeAvro, Bytes: []byte(`{"type":"record","name":"wide","namespace":"public","fields":[` + strings.Join(f, ",") + `]}`)}, sd
}

// BenchmarkLocalTimeCheck is the cost the local-timestamp check adds to every
// record: a schema without a local-timestamp field is skipped with a byte
// search, one with such a field is parsed once and then walked per record.
func BenchmarkLocalTimeCheck(b *testing.B) {
	prev := time.Local
	time.Local = time.UTC
	b.Cleanup(func() { time.Local = prev })
	for _, logical := range []string{"timestamp-micros", "local-timestamp-micros"} {
		b.Run(logical, func(b *testing.B) {
			s, sd := benchSchema(logical)
			var c localTimeCheck
			b.ReportAllocs()
			for b.Loop() {
				if k := c.changed(s, sd); k != "" {
					b.Fatalf("unexpected mismatch in %q", k)
				}
			}
		})
	}
}

// BenchmarkVerifyRoundTrip is the whole lossless check (decode, compare and
// local-timestamp check) on a 30-field record.
func BenchmarkVerifyRoundTrip(b *testing.B) {
	prev := time.Local
	time.Local = time.UTC
	b.Cleanup(func() { time.Local = prev })
	for _, logical := range []string{"timestamp-micros", "local-timestamp-micros"} {
		b.Run(logical, func(b *testing.B) {
			s, sd := benchSchema(logical)
			data, err := s.Marshal(sd)
			if err != nil {
				b.Fatal(err)
			}
			e := &Encoder{}
			b.ReportAllocs()
			for b.Loop() {
				if err := e.verifyRoundTrip(s, sd, data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
