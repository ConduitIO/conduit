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

package smtoracle

import (
	"bytes"
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/goccy/go-json"
)

//go:embed goldens/*.json cases/*.json fixtures/*.json
var files embed.FS

// provenanceFile is the capture metadata in goldens/, not a suite.
const provenanceFile = "provenance.json"

// Result kinds.
const (
	// ResultRecord means the chain emitted a record (Result.Output is set).
	ResultRecord = "record"
	// ResultDropped means the chain returned null: a Filter dropped the record.
	ResultDropped = "dropped"
	// ResultError means a transform threw (Result.Error is set).
	ResultError = "error"
)

// Suite is one golden file: every run of one SMT family over its records.
type Suite struct {
	Suite       string `json:"suite"`
	Description string `json:"description"`
	// RecordType is "sink" (SinkRecord, the default) or "source" (SourceRecord).
	RecordType string                     `json:"recordType"`
	Runs       []Run                      `json:"runs"`
	Schemas    map[string]json.RawMessage `json:"schemas"`
	// CaseSha256 is the SHA-256 of cases/<suite>.json as captured, and
	// FixtureSha256 that of every fixture file the suite reads records from.
	// TestGoldensMatchCaseDefinitions compares them with the committed files,
	// so a case or a fixture edited without a re-capture fails offline.
	CaseSha256    string            `json:"caseSha256"`
	FixtureSha256 map[string]string `json:"fixtureSha256"`
}

// Run is one configured chain applied to every record of the suite.
type Run struct {
	Run     string   `json:"run"`
	Chain   Chain    `json:"chain"`
	Results []Result `json:"results"`
}

// Chain echoes the connector configuration the run used.
type Chain struct {
	Transforms []Transform          `json:"transforms"`
	Predicates map[string]Predicate `json:"predicates"`
}

// Transform is one entry of transforms=, with its predicate wiring.
type Transform struct {
	Alias     string            `json:"alias"`
	Type      string            `json:"type"`
	Config    map[string]string `json:"config"`
	Predicate string            `json:"predicate"`
	Negate    bool              `json:"negate"`
}

// Predicate is one entry of predicates=.
type Predicate struct {
	Type   string            `json:"type"`
	Config map[string]string `json:"config"`
}

// Result is the outcome for one input record.
type Result struct {
	// Record is the name of the input record in cases/<suite>.json.
	Record string `json:"record"`
	// Result is ResultRecord, ResultDropped or ResultError.
	Result string  `json:"result"`
	Output *Record `json:"output,omitempty"`
	Error  *Error  `json:"error,omitempty"`
}

// Record is an emitted Connect record.
type Record struct {
	Topic string `json:"topic"`
	// Partition and Timestamp are nil when the record carries none.
	Partition *int64 `json:"partition"`
	// KafkaOffset is only set for sink records.
	KafkaOffset *int64   `json:"kafkaOffset,omitempty"`
	Timestamp   *int64   `json:"timestamp"`
	Key         Side     `json:"key"`
	Value       Side     `json:"value"`
	Headers     []Header `json:"headers"`
}

// Side is a record key or value. Schema is a key into Suite.Schemas, or nil
// for a schemaless side. Payload is the JSON payload; a null key, value or
// tombstone is the JSON literal null.
type Side struct {
	Schema  *string         `json:"schema"`
	Payload json.RawMessage `json:"payload"`
	// PayloadDefaultConverter is what a worker whose JsonConverter keeps its
	// default (replace.null.with.default=true) would write for this side:
	// the schema's default in place of a null. It is only present where it
	// differs from Payload.
	PayloadDefaultConverter json.RawMessage `json:"payloadDefaultConverter,omitempty"`
}

// Header is one record header. Schema is the Connect type name ("string"),
// or nil for a header without a schema.
type Header struct {
	Key    string          `json:"key"`
	Schema *string         `json:"schema"`
	Value  json.RawMessage `json:"value"`
}

// Error is an exception thrown by a transform. Message has object identity
// hashes replaced by "@<id>".
type Error struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

// Names returns the names of the captured suites, sorted.
func Names() ([]string, error) {
	entries, err := files.ReadDir("goldens")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Name() == provenanceFile {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names, nil
}

// Load returns the captured golden for a suite. Unknown fields in the file
// are an error, so a capture format change cannot pass unnoticed.
func Load(name string) (*Suite, error) {
	var s Suite
	if err := decodeStrict(path.Join("goldens", name+".json"), &s); err != nil {
		return nil, err
	}
	if s.Suite != name {
		return nil, fmt.Errorf("smtoracle: golden %q declares suite %q", name, s.Suite)
	}
	return &s, nil
}

// Find returns the result of the named run for the named input record.
func (s *Suite) Find(run, record string) (*Result, bool) {
	for i := range s.Runs {
		if s.Runs[i].Run != run {
			continue
		}
		for j := range s.Runs[i].Results {
			if s.Runs[i].Results[j].Record == record {
				return &s.Runs[i].Results[j], true
			}
		}
	}
	return nil, false
}

// Schema returns the Connect JSON schema a Side refers to, or nil for a
// schemaless side.
func (s *Suite) Schema(side Side) (json.RawMessage, error) {
	if side.Schema == nil {
		return nil, nil
	}
	raw, ok := s.Schemas[*side.Schema]
	if !ok {
		return nil, fmt.Errorf("smtoracle: suite %q has no schema %q", s.Suite, *side.Schema)
	}
	return raw, nil
}

// Cases is the input of one suite: the records every run is applied to.
type Cases struct {
	Suite       string `json:"suite"`
	Description string `json:"description"`
	RecordType  string `json:"recordType"`
	// Records are the suite-level inputs; a run may override them.
	Records []InputRecord `json:"records"`
	// RecordsFrom names a file in fixtures/ that holds the records instead
	// (the captured Debezium messages).
	RecordsFrom string     `json:"recordsFrom"`
	Runs        []InputRun `json:"runs"`
}

// InputRun is one chain of a suite, with an optional record override.
type InputRun struct {
	Run         string               `json:"run"`
	Chain       []Transform          `json:"chain"`
	Predicates  map[string]Predicate `json:"predicates"`
	RecordType  string               `json:"recordType"`
	Records     []InputRecord        `json:"records"`
	RecordsFrom string               `json:"recordsFrom"`
}

// InputRecord is an input record. Key and Value are JSON (schemaless, with
// the tagged forms for non-JSON Java types), or a payload described by
// KeySchema/ValueSchema in the Kafka Connect JSON schema format.
type InputRecord struct {
	Name        string          `json:"name"`
	Topic       string          `json:"topic"`
	Partition   *int64          `json:"partition"`
	Offset      *int64          `json:"offset"`
	Timestamp   *int64          `json:"timestamp"`
	Key         json.RawMessage `json:"key"`
	KeySchema   json.RawMessage `json:"keySchema"`
	Value       json.RawMessage `json:"value"`
	ValueSchema json.RawMessage `json:"valueSchema"`
	Headers     []InputHeader   `json:"headers"`
}

// InputHeader is an input record header.
type InputHeader struct {
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	Schema string          `json:"schema"`
}

// LoadCases returns the input of a suite.
func LoadCases(name string) (*Cases, error) {
	var c Cases
	if err := decodeStrict(path.Join("cases", name+".json"), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// RecordsFor returns the input records a run is applied to: the run's own,
// else the suite's, each either inline or loaded from fixtures/.
func (c *Cases) RecordsFor(run string) ([]InputRecord, error) {
	recs, from := c.Records, c.RecordsFrom
	for _, r := range c.Runs {
		if r.Run == run && (r.Records != nil || r.RecordsFrom != "") {
			recs, from = r.Records, r.RecordsFrom
		}
	}
	if from == "" {
		return recs, nil
	}
	b, err := Fixture(from)
	if err != nil {
		return nil, err
	}
	var loaded []InputRecord
	if err := json.Unmarshal(b, &loaded); err != nil {
		return nil, fmt.Errorf("smtoracle: decode fixture %s: %w", from, err)
	}
	return loaded, nil
}

// CaseBytes returns the raw bytes of cases/<suite>.json.
func CaseBytes(suite string) ([]byte, error) {
	b, err := files.ReadFile(path.Join("cases", suite+".json"))
	if err != nil {
		return nil, fmt.Errorf("smtoracle: %w", err)
	}
	return b, nil
}

func decodeStrict(file string, v any) error {
	b, err := files.ReadFile(file)
	if err != nil {
		return fmt.Errorf("smtoracle: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("smtoracle: decode %s: %w", file, err)
	}
	return nil
}

// Diff compares two JSON documents by value, ignoring key order and
// whitespace and keeping numbers exact. It returns "" when they are equal,
// otherwise a description of the first difference as a JSON path.
func Diff(want, got json.RawMessage) string {
	w, err := decodeNumber(want)
	if err != nil {
		return "want is not JSON: " + err.Error()
	}
	g, err := decodeNumber(got)
	if err != nil {
		return "got is not JSON: " + err.Error()
	}
	return diffValue("$", w, g)
}

func decodeNumber(b json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func diffValue(p string, w, g any) string {
	switch wv := w.(type) {
	case map[string]any:
		gv, ok := g.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: want object, got %T", p, g)
		}
		keys := map[string]bool{}
		for k := range wv {
			keys[k] = true
		}
		for k := range gv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			a, aok := wv[k]
			b, bok := gv[k]
			switch {
			case !aok:
				return fmt.Sprintf("%s.%s: unexpected key", p, k)
			case !bok:
				return fmt.Sprintf("%s.%s: missing key", p, k)
			}
			if d := diffValue(p+"."+k, a, b); d != "" {
				return d
			}
		}
		return ""
	case []any:
		gv, ok := g.([]any)
		if !ok {
			return fmt.Sprintf("%s: want array, got %T", p, g)
		}
		if len(wv) != len(gv) {
			return fmt.Sprintf("%s: want %d elements, got %d", p, len(wv), len(gv))
		}
		for i := range wv {
			if d := diffValue(fmt.Sprintf("%s[%d]", p, i), wv[i], gv[i]); d != "" {
				return d
			}
		}
		return ""
	case json.Number:
		gv, ok := g.(json.Number)
		if !ok || wv.String() != gv.String() {
			return fmt.Sprintf("%s: want %v, got %v", p, w, g)
		}
		return ""
	default:
		if w != g {
			return fmt.Sprintf("%s: want %v, got %v", p, w, g)
		}
		return ""
	}
}
