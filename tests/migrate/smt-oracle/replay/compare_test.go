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
	// Both sources write the JSON text; key order and spacing may differ.
	kc := norm(t, o, `{"attrs": "{\"b\": [1, 2], \"a\": null}"}`)
	cd := norm(t, o, `{"attrs": "{\"a\":null,\"b\":[1,2]}"}`)
	if d := diffValues("value", kc, cd); len(d) != 0 {
		t.Fatalf("same JSON, different text: %v", d)
	}
	other := norm(t, o, `{"attrs": "{\"a\":null,\"b\":[1,3]}"}`)
	if d := diffValues("value", kc, other); len(d) != 1 || !strings.Contains(d[0], "value.attrs.b[1]") {
		t.Fatalf("different JSON must still differ: %v", d)
	}
	if _, err := o.Normalize(map[string]any{"attrs": "***"}); err == nil {
		t.Fatal("text that is not JSON must fail, not pass through")
	}
	// Empty text is not JSON; it is reported as a difference, not an error (Debezium writes it for a
	// NOT NULL jsonb in a default-identity delete's before-image).
	if d := diffValues("value", norm(t, o, `{"attrs": ""}`), kc); len(d) != 1 {
		t.Fatalf("empty text against JSON must differ: %v", d)
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

func TestNormalize_Toast(t *testing.T) {
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
	// A real value in place of the placeholder is a difference even with the option.
	if d := diffValues("value", norm(t, o, `{"id": 1, "big": "x"}`), cd); len(d) != 1 {
		t.Fatalf("a present column must differ from an absent one: %v", d)
	}
	// Null and absent are different things, always.
	if d := diffValues("value", norm(t, o, `{"id": 1, "x": null}`), norm(t, o, `{"id": 1}`)); len(d) != 1 {
		t.Fatalf("null vs absent must differ: %v", d)
	}
}

func TestNumbersCompareByValue(t *testing.T) {
	// Conduit widens integers and has no float32: 5 and 5.0 are the same number.
	a, _ := decodeNumbers([]byte(`{"n": 5}`))
	b, _ := decodeNumbers([]byte(`{"n": 5.0}`))
	if d := diffValues("v", a, b); len(d) != 0 {
		t.Fatalf("5 vs 5.0: %v", d)
	}
	c, _ := decodeNumbers([]byte(`{"n": 5.5}`))
	if d := diffValues("v", a, c); len(d) != 1 {
		t.Fatalf("5 vs 5.5 must differ: %v", d)
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
	rec := View{Kind: ViewRecord, InputTopic: "t", Topic: "t", Value: map[string]any{"a": json.Number("1")}}
	if d, _ := Compare(rec, View{Kind: ViewDropped}, Options{}); len(d) != 1 {
		t.Fatalf("record vs dropped: %v", d)
	}
	if d, _ := Compare(View{Kind: ViewDropped}, View{Kind: ViewDropped}, Options{}); len(d) != 0 {
		t.Fatalf("dropped vs dropped: %v", d)
	}
	// A deleted row: Kafka Connect's null value and Conduit's nil After are the same.
	cd, err := FromConduit(Outcome{Decoded: opencdc.Record{Key: opencdc.StructuredData{"id": 1}}}, Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	kc := View{Kind: ViewRecord, InputTopic: "t", Topic: "t", Key: map[string]any{"id": json.Number("1")}}
	if d, _ := Compare(kc, cd, Options{}); len(d) != 0 {
		t.Fatalf("delete: %v", d)
	}
	// A value that is {} on one side and null on the other is a difference (#3147).
	cd.Value = map[string]any{}
	if d, _ := Compare(kc, cd, Options{}); len(d) != 1 {
		t.Fatalf("null vs {}: %v", d)
	}
}

func TestCompare_Topic(t *testing.T) {
	kc := View{Kind: ViewRecord, InputTopic: "dbz.public.items", Topic: "cdc.items"}
	if d, _ := Compare(kc, View{Kind: ViewRecord}, Options{}); len(d) != 1 || !strings.Contains(d[0], "supplied no topic") {
		t.Fatalf("a rewritten topic with none supplied must fail closed: %v", d)
	}
	if d, _ := Compare(kc, View{Kind: ViewRecord, Topic: "cdc.items"}, Options{}); len(d) != 0 {
		t.Fatalf("the right topic: %v", d)
	}
	if d, _ := Compare(kc, View{Kind: ViewRecord, Topic: "cdc.other"}, Options{}); len(d) != 1 {
		t.Fatalf("the wrong topic: %v", d)
	}
	same := View{Kind: ViewRecord, InputTopic: "t", Topic: "t"}
	if d, _ := Compare(same, View{Kind: ViewRecord}, Options{}); len(d) != 0 {
		t.Fatalf("an untouched topic needs none supplied: %v", d)
	}
	if d, _ := Compare(same, View{Kind: ViewRecord, Topic: "u"}, Options{}); len(d) != 1 {
		t.Fatalf("a topic changed by the translation but not by Kafka Connect: %v", d)
	}
}

func TestCompare_Headers(t *testing.T) {
	kc := View{Kind: ViewRecord, InputTopic: "t", Topic: "t", Headers: map[string]any{"__op": "c"}}
	if d, _ := Compare(kc, View{Kind: ViewRecord, Topic: "t", Headers: map[string]any{}}, Options{}); len(d) != 1 {
		t.Fatalf("a missing header: %v", d)
	}
	if d, _ := Compare(kc, View{Kind: ViewRecord, Topic: "t", Headers: map[string]any{"__op": "c"}}, Options{}); len(d) != 0 {
		t.Fatalf("a matching header: %v", d)
	}
	if d, _ := Compare(kc, View{Kind: ViewRecord, Topic: "t", Headers: map[string]any{"__op": "u"}}, Options{}); len(d) != 1 {
		t.Fatalf("a wrong header value: %v", d)
	}
	// A header Conduit adds that Kafka Connect did not is a difference too.
	none := View{Kind: ViewRecord, InputTopic: "t", Topic: "t", Headers: map[string]any{}}
	if d, _ := Compare(none, View{Kind: ViewRecord, Topic: "t", Headers: map[string]any{"x": "y"}}, Options{}); len(d) != 1 {
		t.Fatalf("an extra header: %v", d)
	}
}

func TestCompare_Errors(t *testing.T) {
	kc := View{Kind: ViewError, ErrClass: "org.apache.kafka.connect.errors.DataException", ErrMessage: "Only Struct objects supported, found: null"}
	cd := View{Kind: ViewError, ErrMessage: "field.set.missing_field: .Payload.After.name"}
	d, _ := Compare(kc, cd, Options{})
	if len(d) != 1 || !strings.Contains(d[0], "no ErrorMatch") || !strings.Contains(d[0], "field.set.missing_field") {
		t.Fatalf("an undeclared error pair must be reported with both texts: %v", d)
	}
	ok := Options{ErrorMatch: []ErrorMatch{{KCClass: "DataException", KCMessage: "found: null", Conduit: "field.set.missing_field"}}}
	if d, _ := Compare(kc, cd, ok); len(d) != 0 {
		t.Fatalf("a declared pair: %v", d)
	}
	wrong := View{Kind: ViewError, ErrMessage: "template: bad function"}
	if d, _ := Compare(kc, wrong, ok); len(d) != 1 {
		t.Fatalf("a Conduit failure for the wrong reason must differ: %v", d)
	}
}

func TestCompare_ErrorMatchCannotMatchEverything(t *testing.T) {
	kc := View{Kind: ViewError, ErrClass: "org.apache.kafka.connect.errors.DataException", ErrMessage: "boom"}
	cd := View{Kind: ViewError, ErrMessage: "template: bad function"}
	for name, m := range map[string]ErrorMatch{
		"empty Conduit text": {KCClass: "DataException"},
		"empty class":        {Conduit: "template"},
		"empty entry":        {},
	} {
		d, _ := Compare(kc, cd, Options{ErrorMatch: []ErrorMatch{m}})
		if len(d) != 1 || !strings.Contains(d[0], "would match every failure") {
			t.Fatalf("%s must be rejected: %v", name, d)
		}
	}
	// A class suffix is not a class: "Exception" must not match DataException.
	d, _ := Compare(kc, cd, Options{ErrorMatch: []ErrorMatch{{KCClass: "Exception", Conduit: "template"}}})
	if len(d) != 1 || !strings.Contains(d[0], "no ErrorMatch") {
		t.Fatalf("a class suffix must not match: %v", d)
	}
	// The simple name and the full name both match.
	for _, class := range []string{"DataException", "org.apache.kafka.connect.errors.DataException"} {
		if d, _ := Compare(kc, cd, Options{ErrorMatch: []ErrorMatch{{KCClass: class, Conduit: "template"}}}); len(d) != 0 {
			t.Fatalf("%s: %v", class, d)
		}
	}
}

func TestCompareTombstone(t *testing.T) {
	kept := View{Kind: ViewRecord}
	if d := CompareTombstone(kept, true); len(d) != 0 {
		t.Fatalf("kept as expected: %v", d)
	}
	if d := CompareTombstone(View{Kind: ViewDropped}, true); len(d) != 1 {
		t.Fatalf("dropped but the translation keeps it: %v", d)
	}
	if d := CompareTombstone(View{Kind: ViewDropped}, false); len(d) != 0 {
		t.Fatalf("dropped as expected: %v", d)
	}
	if d := CompareTombstone(View{Kind: ViewError, ErrClass: "X"}, false); len(d) != 1 {
		t.Fatalf("a failed tombstone is reported: %v", d)
	}
}

func TestFromKafkaConnect(t *testing.T) {
	in := smtoracle.InputRecord{Topic: "in", Headers: []smtoracle.InputHeader{{Key: "keep", Value: json.RawMessage(`"x"`)}, {Key: "change", Value: json.RawMessage(`"old"`)}}}
	v, err := FromKafkaConnect(smtoracle.Result{Result: smtoracle.ResultRecord, Output: &smtoracle.Record{
		Topic: "out",
		Key:   smtoracle.Side{Payload: json.RawMessage(`{"id": {"$int32": 1}}`)},
		Value: smtoracle.Side{Payload: json.RawMessage(`null`)},
		Headers: []smtoracle.Header{
			{Key: "keep", Value: json.RawMessage(`"x"`)},
			{Key: "change", Value: json.RawMessage(`"new"`)},
			{Key: "added", Value: json.RawMessage(`"y"`)},
		},
	}}, in)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != ViewRecord || v.Value != nil || v.Topic != "out" || v.InputTopic != "in" {
		t.Fatalf("unexpected view: %+v", v)
	}
	if len(v.Headers) != 2 || v.Headers["change"] != "new" || v.Headers["added"] != "y" {
		t.Fatalf("only changed and added headers belong in the view: %v", v.Headers)
	}
	if v.Key.(map[string]any)["id"] != json.Number("1") {
		t.Fatalf("tag not removed: %v", v.Key)
	}
	if _, err := FromKafkaConnect(smtoracle.Result{Result: "nope"}, in); err == nil {
		t.Fatal("unknown result must fail")
	}
}
