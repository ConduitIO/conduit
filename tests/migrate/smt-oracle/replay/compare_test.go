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

package replay

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/conduitio/conduit-commons/opencdc"
	smtoracle "github.com/conduitio/conduit/tests/migrate/smt-oracle"
)

func norm(t *testing.T, o Options, in string) map[string]any {
	t.Helper()
	v, err := decodeNumbers([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := o.Normalize(v)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}

// Each normalization has a test that shows both representations becoming the same value,
// and one that shows a real difference surviving it.

func TestNormalize_JSON(t *testing.T) {
	o := Options{JSONFields: []string{"attrs"}}
	// Debezium: the JSON text. Conduit: base64 of the same text. Key order and spacing differ.
	kc := norm(t, o, `{"attrs": "{\"b\": [1, 2], \"a\": null}"}`)
	cd := norm(t, o, `{"attrs": "eyJhIjpudWxsLCJiIjpbMSwyXX0="}`) // {"a":null,"b":[1,2]}
	if d := diffValues("value", kc, cd); len(d) != 0 {
		t.Fatalf("same JSON, different representation: %v", d)
	}
	other := norm(t, o, `{"attrs": "eyJhIjpudWxsLCJiIjpbMSwzXX0="}`) // {"a":null,"b":[1,3]}
	if d := diffValues("value", kc, other); len(d) != 1 || !strings.Contains(d[0], "value.attrs.b[1]") {
		t.Fatalf("different JSON must still differ: %v", d)
	}
	if _, err := o.Normalize(map[string]any{"attrs": "***"}); err == nil {
		t.Fatal("a value that is neither JSON nor base64 must fail, not pass through")
	}
}

func TestNormalize_Decimal(t *testing.T) {
	o := Options{DecimalFields: map[string]int{"price": 2}}
	kc := norm(t, o, `{"price": 12.5}`)
	cd := norm(t, o, `{"price": "25/2"}`) // Conduit renders numeric as a big.Rat
	if d := diffValues("value", kc, cd); len(d) != 0 {
		t.Fatalf("12.50 and 25/2: %v", d)
	}
	if d := diffValues("value", norm(t, o, `{"price": 9.99}`), norm(t, o, `{"price": "999/100"}`)); len(d) != 0 {
		t.Fatalf("9.99 and 999/100: %v", d)
	}
	if d := diffValues("value", norm(t, o, `{"price": 9.99}`), norm(t, o, `{"price": "1000/100"}`)); len(d) != 1 {
		t.Fatalf("9.99 and 10.00 must differ: %v", d)
	}
	if _, err := o.Normalize(map[string]any{"price": true}); err == nil {
		t.Fatal("a non-decimal must fail")
	}
}

func TestNormalize_Timestamp(t *testing.T) {
	o := Options{TimestampFields: []string{"created"}}
	kc := norm(t, o, `{"created": "2023-11-14T22:13:20.000000Z"}`)
	cd := norm(t, o, `{"created": "2023-11-14T22:13:20Z"}`)
	if d := diffValues("value", kc, cd); len(d) != 0 {
		t.Fatalf("trailing zeros: %v", d)
	}
	offset := norm(t, o, `{"created": "2023-11-15T03:43:20+05:30"}`) // the same instant
	if d := diffValues("value", kc, offset); len(d) != 0 {
		t.Fatalf("same instant, other offset: %v", d)
	}
	later := norm(t, o, `{"created": "2023-11-14T22:13:20.000001Z"}`)
	if d := diffValues("value", kc, later); len(d) != 1 {
		t.Fatalf("one microsecond apart must differ: %v", d)
	}
}

func TestNormalize_ToastAndNull(t *testing.T) {
	o := Options{ToastAbsent: true}
	kc := norm(t, o, `{"id": 1, "big": "__debezium_unavailable_value"}`)
	cd := norm(t, o, `{"id": 1}`)
	if d := diffValues("value", kc, cd); len(d) != 0 {
		t.Fatalf("unchanged TOAST column: %v", d)
	}
	// Without the option the difference is reported, so it is never hidden by accident.
	if d := diffValues("value", norm(t, Options{}, `{"id": 1, "big": "__debezium_unavailable_value"}`), cd); len(d) != 1 {
		t.Fatalf("placeholder without ToastAbsent must differ: %v", d)
	}
	n := Options{NullAsAbsent: true}
	if d := diffValues("value", norm(t, n, `{"id": 1, "x": null}`), norm(t, n, `{"id": 1}`)); len(d) != 0 {
		t.Fatalf("null vs absent: %v", d)
	}
	if d := diffValues("value", norm(t, Options{}, `{"id": 1, "x": null}`), norm(t, Options{}, `{"id": 1}`)); len(d) != 1 {
		t.Fatalf("null vs absent without the option must differ: %v", d)
	}
}

func TestUntag(t *testing.T) {
	v, err := untag(json.RawMessage(`{"a": {"$int32": 5}, "d": {"$date": "2023-11-14T22:13:20.123Z"}, "b": {"$bytes": "AQID"}, "n": {"x": 1}, "l": [{"$int8": 1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := decodeNumbers([]byte(`{"a": 5, "d": "2023-11-14T22:13:20.123Z", "b": "AQID", "n": {"x": 1}, "l": [1]}`))
	if d := diffValues("v", v, want); len(d) != 0 {
		t.Fatalf("untag: %v", d)
	}
	// A one-key object that merely looks tagged is left alone.
	v, _ = untag(json.RawMessage(`{"$other": 1}`))
	if m, ok := v.(map[string]any); !ok || len(m) != 1 {
		t.Fatalf("unknown tag must be preserved: %v", v)
	}
}

func TestCompare_Outcomes(t *testing.T) {
	rec := View{Kind: ViewRecord, Value: map[string]any{"a": json.Number("1")}}
	if d, _ := Compare(rec, View{Kind: ViewDropped}, Options{}); len(d) != 1 {
		t.Fatalf("record vs dropped: %v", d)
	}
	if d, _ := Compare(View{Kind: ViewDropped}, View{Kind: ViewDropped}, Options{}); len(d) != 0 {
		t.Fatalf("dropped vs dropped: %v", d)
	}
	// A deleted row: Kafka Connect's null value and Conduit's nil After are the same.
	cd, err := FromConduit(Outcome{Decoded: opencdc.Record{Key: opencdc.StructuredData{"id": 1}}})
	if err != nil {
		t.Fatal(err)
	}
	kc := View{Kind: ViewRecord, Key: map[string]any{"id": json.Number("1")}}
	if d, _ := Compare(kc, cd, Options{}); len(d) != 0 {
		t.Fatalf("delete: %v", d)
	}
	// A value that is {} on one side and null on the other is a difference (#3147).
	cd.Value = map[string]any{}
	if d, _ := Compare(kc, cd, Options{}); len(d) != 1 {
		t.Fatalf("null vs {}: %v", d)
	}
}

func TestFromKafkaConnect(t *testing.T) {
	v, err := FromKafkaConnect(smtoracle.Result{Result: smtoracle.ResultRecord, Output: &smtoracle.Record{
		Topic:   "t",
		Key:     smtoracle.Side{Payload: json.RawMessage(`{"id": {"$int32": 1}}`)},
		Value:   smtoracle.Side{Payload: json.RawMessage(`null`)},
		Headers: []smtoracle.Header{{Key: "h", Value: json.RawMessage(`"x"`)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != ViewRecord || v.Value != nil || v.Topic != "t" || v.Headers["h"] != "x" {
		t.Fatalf("unexpected view: %+v", v)
	}
	if v.Key.(map[string]any)["id"] != json.Number("1") {
		t.Fatalf("tag not removed: %v", v.Key)
	}
	if _, err := FromKafkaConnect(smtoracle.Result{Result: "nope"}); err == nil {
		t.Fatal("unknown result must fail")
	}
}
