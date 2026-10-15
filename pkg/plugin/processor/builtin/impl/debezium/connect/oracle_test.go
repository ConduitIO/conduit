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

package connect

import (
	"bufio"
	"compress/gzip"
	"encoding/hex"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

// The converter oracle: tests/debezium/oracle/Oracle.java run with the real
// Kafka Connect JsonConverter and SimpleHeaderConverter inside each pinned
// Debezium image (gen.sh). Each row holds the hex bytes Kafka wrote; this
// test requires the Go port to write the same bytes, or to fail exactly where
// Kafka fails.
var oracleDir = filepath.Join("..", "..", "..", "..", "..", "..", "..", "tests", "debezium", "oracle")

func oracleHex(b []byte, err error) string {
	switch {
	case err != nil:
		return "ERR"
	case b == nil:
		return "NULL"
	case len(b) == 0:
		return "-"
	}
	return hex.EncodeToString(b)
}

func oracleWant(s string) string {
	if strings.HasPrefix(s, "ERR:") {
		return "ERR"
	}
	return s
}

func oracleBytes(t *testing.T, s string) []byte {
	t.Helper()
	if s == "-" {
		return []byte{}
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConverterOracle(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(oracleDir, "out-*.tsv.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no oracle files in %s (tests/debezium/oracle/gen.sh): %v", oracleDir, err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			runOracle(t, path)
		})
	}
}

//nolint:gocyclo // one case per oracle row kind
func runOracle(t *testing.T, path string) {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	js := NewJSONConverter()
	jn := NewJSONConverter()
	jn.SchemasEnable = false
	jnum := jn
	jnum.DecimalFormat = DecimalNumeric
	jsnum := js
	jsnum.DecimalFormat = DecimalNumeric
	hc := SimpleHeaderConverter{}

	failures := 0
	check := func(row, what, got, want string) {
		if got == oracleWant(want) {
			return
		}
		failures++
		if failures <= 10 {
			g, _ := hex.DecodeString(got)
			w, _ := hex.DecodeString(want)
			t.Errorf("%s %s:\n got  %q (%s)\n want %q (%s)", row, what, g, got, w, want)
		}
	}
	kinds := map[string]int{}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		c := strings.Split(sc.Text(), "\t")
		kind, in := c[0], c[1]
		kinds[kind]++
		row := kind + " " + in
		var s *Schema
		var v any
		switch kind {
		case "D":
			bits, _ := strconv.ParseUint(in, 16, 64)
			s, v = &Schema{Type: TypeFloat64}, math.Float64frombits(bits)
		case "F":
			bits, _ := strconv.ParseUint(in, 16, 32)
			s, v = &Schema{Type: TypeFloat32}, math.Float32frombits(uint32(bits))
		case "L":
			i, _ := strconv.ParseInt(in, 10, 64)
			s, v = &Schema{Type: TypeInt64}, i
		case "S":
			s, v = &Schema{Type: TypeString}, string(oracleBytes(t, in))
		case "N":
			p := strings.Split(in, ",")
			u, _ := new(big.Int).SetString(p[0], 10)
			scale, _ := strconv.Atoi(p[1])
			one := 1
			ds := &Schema{Type: TypeBytes, Name: LogicalDecimal, Version: &one, Parameters: []Param{{"scale", p[1]}}}
			d := Decimal{Unscaled: u, Scale: int32(scale)}
			check(row, "json-base64", oracleHex(jn.Marshal(ds, d)), c[2])
			check(row, "json-numeric", oracleHex(jnum.Marshal(ds, d)), c[3])
			check(row, "header", oracleHex(hc.Marshal(ds, d)), c[4])
			check(row, "json-schema-numeric", oracleHex(jsnum.Marshal(ds, d)), c[5])
			// ParsePayloadJSON is the converter's inverse: Kafka's bytes
			// parse back to a value that encodes to the same bytes, in both
			// decimal formats.
			for _, conv := range []JSONConverter{jn, jnum} {
				want := c[2]
				if conv.DecimalFormat == DecimalNumeric {
					want = c[3]
				}
				goVal, err := ParsePayloadJSON(ds, oracleBytes(t, want), conv.DecimalFormat)
				if err != nil {
					t.Errorf("%s: ParsePayloadJSON(%s): %v", row, conv.DecimalFormat, err)
					continue
				}
				cv, err := FromGo(ds, goVal)
				if err != nil {
					t.Errorf("%s: FromGo(%v): %v", row, goVal, err)
					continue
				}
				check(row, "inverse-"+string(conv.DecimalFormat), oracleHex(conv.Marshal(ds, cv)), want)
			}
			continue
		case "STRUCT", "DEFAULTS", "TOPNULL", "EMPTYPARAMS":
			s = oracleSchema(t, oracleBytes(t, c[2]))
			switch kind {
			case "STRUCT":
				cv, err := FromGo(s, map[string]any{
					"f\"\\é😀": "val\"\\\x01😀",
					"b":       []byte{0, 255},
					"arr":     []any{math.NaN(), math.Copysign(0, -1), 1e300},
					"m":       []MapEntry{{"z\"", int32(1)}, {"a", nil}, {"😀", int32(3)}},
					"mi":      []MapEntry{{int32(2), "x\\y"}, {int32(-1), "\x00"}},
				})
				if err != nil {
					t.Fatalf("STRUCT FromGo: %v", err)
				}
				v = cv
			case "DEFAULTS":
				cv, err := FromGo(s, map[string]any{"arr": []any{int32(1), nil}})
				if err != nil {
					t.Fatalf("DEFAULTS FromGo: %v", err)
				}
				check(row, "json-schema", oracleHex(js.Marshal(s, cv)), c[2])
				check(row, "json-schema-numeric", oracleHex(jsnum.Marshal(s, cv)), c[3])
				check(row, "header", oracleHex(hc.Marshal(s, cv)), c[4])
				continue
			case "EMPTYPARAMS":
				s.Parameters = []Param{} // present but empty, as the oracle built it
				check(row, "json-schema", oracleHex(js.Marshal(s, "x")), c[2])
				continue
			case "TOPNULL":
				v = nil
			}
		case "DEPTH":
			depth, _ := strconv.Atoi(in)
			s = &Schema{Type: TypeInt32}
			v = int64(7)
			for i := 0; i < depth; i++ {
				s = &Schema{Type: TypeArray, Items: s}
				v = []any{v}
			}
			gotN := oracleHex(jn.Marshal(s, v))
			lenPrefix, rest, _ := strings.Cut(c[2], ":")
			if strings.HasPrefix(rest, "ERR") {
				check(row, "json", gotN, "ERR:")
			} else if strconv.Itoa(len(gotN)) != lenPrefix || !strings.HasPrefix(gotN, rest) {
				t.Errorf("%s json: got %d hex chars, want %s", row, len(gotN), lenPrefix)
			}
			_, errS := js.Marshal(s, v)
			if wantErr := c[3] == "true"; (errS != nil) != wantErr {
				t.Errorf("%s json-schema: error %v, Kafka failed=%v", row, errS, wantErr)
			}
			continue
		default:
			t.Fatalf("unknown oracle row kind %q", kind)
		}
		check(row, "json-schema", oracleHex(js.Marshal(s, v)), c[2])
		check(row, "json", oracleHex(jn.Marshal(s, v)), c[3])
		check(row, "header", oracleHex(hc.Marshal(s, v)), c[4])
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"D", "F", "L", "S", "N", "STRUCT", "DEFAULTS", "TOPNULL", "DEPTH", "EMPTYPARAMS"} {
		if kinds[k] == 0 {
			t.Errorf("oracle has no %s rows", k)
		}
	}
	if failures > 0 {
		t.Errorf("%d oracle mismatches (rows per kind: %v)", failures, kinds)
	}
}

func oracleSchema(t *testing.T, full []byte) *Schema {
	t.Helper()
	var env struct {
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(full, &env); err != nil {
		t.Fatal(err)
	}
	s, err := ParseSchemaJSON(env.Schema)
	if err != nil {
		t.Fatalf("parse oracle schema: %v", err)
	}
	return s
}
