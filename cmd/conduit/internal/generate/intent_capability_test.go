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

package generate

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	procbuiltin "github.com/conduitio/conduit/pkg/plugin/processor/builtin"
	"github.com/matryer/is"
)

// Every corpus prompt must extract exactly the capabilities the corpus says
// it requires. Missing one lets a pipeline that dropped the processor pass
// the judge (#2935); an extra one rejects correct pipelines and steers the
// retry toward a processor nobody asked for (#2936, and "anywhere" read as
// "where" → filter).
func TestExtractIntent_CorpusCapabilitiesMatchTheCorpus(t *testing.T) {
	names := CatalogNames(BuiltinCatalog())
	requests, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range requests {
		got := ExtractIntent(r.Prompt, names).Capabilities
		want := r.Expect.RequiredCapabilities
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("id=%q: extracted %v, corpus requires %v\n  prompt=%q", r.ID, got, want, r.Prompt)
		}
	}
}

// Paraphrases per capability. The corpus wording is one row in each group;
// the rest are wordings the corpus does not use, so a rule tuned to the
// corpus string alone fails here. Negative rows are near-misses that must
// NOT require the capability.
func TestExtractCapabilities_Paraphrases(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want []string // prompts that must require tag
		not  []string // prompts that must not
	}{{
		tag: capMask,
		want: []string{
			"export postgres customer records to s3, but remove the ssn field before writing", // corpus
			"drop the password column before writing to kafka",
			"strip out the email and phone fields",
			"the ssn field should be removed before it reaches s3",
			"exclude the internal_notes field from every record",
			"mask the credit card number",
			"redact pii before publishing",
		},
		not: []string{
			"remove duplicates before writing to s3",
			"drop the table after the export finishes",
			"exclude rows where status is cancelled",
		},
	}, {
		tag: capRename,
		want: []string{
			"archive kafka messages to s3, renaming the 'ts' field to 'timestamp' before writing", // corpus
			"rename ts to timestamp",
			"the user_id column gets renamed to customer_id",
		},
	}, {
		tag: capSet,
		want: []string{
			"stream postgres orders to kafka, adding a derived 'processed_at' field with the current timestamp before publishing", // corpus
			"set the region field to eu-west-1 on every record",
			"add a source field with the value postgres",
			"populate the ingested_at attribute with the current time",
		},
		not: []string{
			"set up a pipeline from postgres to kafka",
			"add more partitions to the kafka topic",
		},
	}, {
		tag: capConvert,
		want: []string{
			"stream postgres orders to kafka, converting the amount field to a floating point number before publishing", // corpus
			"convert the price field to an integer",
			"cast the quantity column to a string",
		},
		not: []string{
			"convert this kafka connect setup to conduit",
		},
	}, {
		tag: capSplit,
		want: []string{
			"archive kafka messages to s3, splitting any batched records into individual records first", // corpus
			"split each batch into separate messages",
			"the payload holds an array of events; split them before writing",
		},
		not: []string{
			"split the traffic between two s3 buckets",
		},
	}, {
		tag: capUnwrapKafkaconnect,
		want: []string{
			"consume kafka connect formatted messages from a topic and upsert them into postgres, unwrapping the kafka connect envelope first", // corpus
			"strip the kafka connect envelope and write the rows to postgres",
			"unwrap kafkaconnect records before the s3 sink",
		},
		not: []string{
			"migrate my kafka connect deployment to conduit",
			"read from kafka and connect it to postgres",
		},
	}, {
		tag: capUnwrapDebezium,
		want: []string{
			"capture postgres change data and publish it to kafka, unwrapping the debezium envelope so the topic only has the actual row data", // corpus
			"unwrap the debezium records before writing to postgres",
		},
		not: []string{
			"publish debezium formatted change events to kafka",
		},
	}, {
		tag: capFilter,
		want: []string{
			"stream new orders from postgres into a kafka topic, only orders over $100",                     // corpus
			"consume kafka events and upsert them into postgres, but skip any event marked as a test event", // corpus
			"stream postgres orders to kafka as json, only include orders that are still pending",           // corpus
			"filter out test events",
			"only keep rows where status is active",
			"skipping heartbeat messages, copy kafka to s3",
		},
		not: []string{
			"tap postgres change events and print them to the log for debugging, don't write them anywhere else", // corpus
			"read new objects from an s3 bucket and print an audit line to the log for each one, don't store them anywhere",
			"unwrap the debezium envelope so the topic only has the actual row data",
			"the target is somewhere in s3",
		},
	}, {
		tag: capJSONEncode,
		want: []string{
			"export all customer records from postgres to an s3 bucket as json", // corpus
			"archive every message from kafka into an s3 bucket as json files",  // corpus
			"encode the payload to json before publishing",
			"serialize records into json",
		},
		not: []string{
			"parse the json payload before writing to postgres",
			"write json encoded records to kafka", // describes the data, no direction
		},
	}, {
		tag: capJSONDecode,
		want: []string{
			"parse the json payload before writing to postgres",
			"decode json messages from kafka",
			"kafka messages are json strings; decode them into structured data",
		},
		not: []string{
			"export everything as json",
		},
	}, {
		tag:  capAvroEncode,
		want: []string{"publish records to kafka as avro", "encode each message in avro"},
		not:  []string{"decode avro messages from kafka"},
	}, {
		tag:  capAvroDecode,
		want: []string{"decode avro messages from kafka", "deserialize the avro payload"},
	}} {
		t.Run(tc.tag, func(t *testing.T) {
			for _, p := range tc.want {
				if got := extractCapabilities(p); !slices.Contains(got, tc.tag) {
					t.Errorf("want %q, got %v\n  prompt=%q", tc.tag, got, p)
				}
			}
			for _, p := range tc.not {
				if got := extractCapabilities(p); slices.Contains(got, tc.tag) {
					t.Errorf("must not require %q, got %v\n  prompt=%q", tc.tag, got, p)
				}
			}
		})
	}
}

// #2936: any "base64" was read as base64-encode, so every correct answer to
// a decode request was rejected. Direction comes from unambiguous wording,
// and a bare "base64" requires nothing.
func TestExtractCapabilities_Base64Direction(t *testing.T) {
	for _, tc := range []struct {
		prompt string
		want   []string
	}{{
		prompt: "read a file of base64-encoded records and write the decoded content to another file", // corpus
		want:   []string{capBase64Decode},
	}, {
		prompt: "decode the base64 payload before writing to postgres",
		want:   []string{capBase64Decode},
	}, {
		prompt: "kafka messages arrive base64 encoded; decoding them is required before the s3 sink",
		want:   []string{capBase64Decode},
	}, {
		prompt: "encode the payload as base64 before publishing to kafka",
		want:   []string{capBase64Encode},
	}, {
		prompt: "write each record to s3 base64-encoded", // describes the data, no direction
		want:   nil,
	}, {
		prompt: "read base64-encoded json from kafka and decode the json", // base64 gives no direction
		want:   []string{capJSONDecode},
	}, {
		prompt: "base64 encode the body field",
		want:   []string{capBase64Encode},
	}, {
		prompt: "decode the base64 field and write the result as json",
		want:   []string{capBase64Decode, capJSONEncode},
	}, {
		prompt: "something about base64 and kafka",
		want:   nil,
	}} {
		got := extractCapabilities(tc.prompt)
		if !slices.Equal(got, tc.want) {
			t.Errorf("got %v, want %v\n  prompt=%q", got, tc.want, tc.prompt)
		}
	}
}

// End to end through Generate's real judge, with candidates the model
// actually produced in the #2928 capture (testdata/candidates/*-captured-*;
// the "missing" variants of set/convert/rename are the captured pipeline with
// its processors block removed, the shape the live eval accepted 3/3).
// Before this fix every "missing" candidate was accepted, the base64 decode
// one was rejected, and the two "anywhere" prompts demanded a filter.
func TestGenerate_JudgeOnCapturedCandidates(t *testing.T) {
	prompts := map[string]string{}
	requests, err := LoadRequests("testdata/eval_requests.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range requests {
		prompts[r.ID] = r.Prompt
	}

	for _, tc := range []struct {
		id, file string
		accept   bool
	}{
		{"postgres-to-s3-mask-pii", "postgres-to-s3-mask-pii-captured-missing-mask.yaml", false},
		{"kafka-connect-unwrap-to-postgres", "kafka-connect-unwrap-to-postgres-captured-missing-unwrap.yaml", false},
		{"kafka-to-s3-split-batches", "kafka-to-s3-split-batches-captured-missing-split.yaml", false},
		{"postgres-to-kafka-set-derived-field", "postgres-to-kafka-set-derived-field-captured-missing-set.yaml", false},
		{"postgres-to-kafka-set-derived-field", "postgres-to-kafka-set-derived-field-captured-good.yaml", true},
		{"postgres-to-kafka-convert-types", "postgres-to-kafka-convert-types-captured-missing-convert.yaml", false},
		{"postgres-to-kafka-convert-types", "postgres-to-kafka-convert-types-captured-good.yaml", true},
		{"kafka-to-s3-rename-field", "kafka-to-s3-rename-field-captured-missing-rename.yaml", false},
		{"kafka-to-s3-rename-field", "kafka-to-s3-rename-field-captured-good.yaml", true},
		{"file-to-file-base64-decode", "file-to-file-base64-decode-captured-missing-decode.yaml", false},
		{"file-to-file-base64-decode", "file-to-file-base64-decode-captured-good.yaml", true},
		{"postgres-to-log-debug-tap", "postgres-to-log-debug-tap-captured-good.yaml", true},
		{"s3-to-log-audit", "s3-to-log-audit-captured-good.yaml", true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			is := is.New(t)
			prompt, ok := prompts[tc.id]
			is.True(ok) // fixture names a corpus id
			candidate, err := os.ReadFile("testdata/candidates/" + tc.file)
			is.NoErr(err)

			p := &fakeProvider{replies: []string{string(candidate)}}
			res, err := Generate(context.Background(), Input{Prompt: prompt, Provider: p, MaxAttempts: 1})
			is.Equal(len(res.Attempts), 1)
			is.True(res.Attempts[0].Report.OK()) // every fixture validates; only the judge differs
			if tc.accept {
				is.NoErr(err)
				return
			}
			ce, ok := conduiterr.Get(err)
			is.True(ok)
			is.Equal(ce.Code, CodeSemanticMismatch)
		})
	}
}

// Every tag a rule can produce must be one capability.go can satisfy, and
// every processor capability.go names must exist in this binary. An unknown
// tag is permanently unsatisfiable, and an unknown plugin can never be
// produced by a valid candidate.
func TestCapabilityRules_ResolveToRealTags(t *testing.T) {
	is := is.New(t)

	var tags []string
	for _, r := range fieldActionRules {
		tags = append(tags, r.tag)
	}
	for _, f := range codecFormats {
		tags = append(tags, f.encode, f.decode)
	}
	for _, f := range envelopeFormats {
		tags = append(tags, f.tag)
	}
	for _, r := range wordRules {
		tags = append(tags, r.tag)
	}
	tags = append(tags, capSplit, capFilter)
	for _, tag := range tags {
		_, ok := capabilityProcessors[tag]
		is.True(ok) // tag has processors
	}

	refs := BuiltinProcessorRefs()
	for tag, plugins := range capabilityProcessors {
		for plugin := range plugins {
			if _, ok := refs[plugin]; !ok {
				t.Errorf("capability %q names processor %q, which is not a builtin", tag, plugin)
			}
		}
	}
}

// The verbs that recognize a processor start from the processor itself: the
// action in its plugin name (field.exclude → exclude, base64.decode → decode)
// and the first word of its spec summary ("Remove a subset of fields" →
// remove). If a processor is renamed or its summary reworded, this fails and
// the rule is updated with it, instead of drifting into a private vocabulary.
func TestCapabilityRules_VerbsComeFromProcessorSpecs(t *testing.T) {
	// Each rule's verbs, keyed by the capability tag the rule produces. The
	// plugins come from capabilityProcessors, so every processor a rule can
	// be satisfied by is checked.
	verbsFor := map[string][]string{capSplit: splitRule.verbs}
	for _, r := range fieldActionRules {
		verbsFor[r.tag] = r.verbs
	}
	for _, f := range codecFormats {
		verbsFor[f.encode] = encodeVerbs
		verbsFor[f.decode] = decodeVerbs
	}
	for _, f := range envelopeFormats {
		verbsFor[f.tag] = unwrapVerbs
	}

	for tag, verbs := range verbsFor {
		forms := inflectAll(verbs)
		for plugin := range capabilityProcessors[tag] {
			// The action is one segment of the name: the last for
			// "field.exclude" and "json.decode", the first for
			// "unwrap.debezium".
			segments := strings.Split(plugin, ".")
			if !slices.ContainsFunc(segments, func(s string) bool { return forms[s] }) {
				t.Errorf("%s (%s): rule verbs %v include no segment of the plugin name", plugin, tag, verbs)
			}

			ctor, ok := procbuiltin.DefaultBuiltinProcessors[plugin]
			if !ok {
				t.Errorf("%s: not a builtin processor", plugin)
				continue
			}
			spec, err := ctor(log.Nop()).Specification()
			if err != nil {
				t.Fatalf("%s: spec: %v", plugin, err)
			}
			summaryVerb := strings.ToLower(strings.Fields(spec.Summary)[0])
			if !forms[summaryVerb] {
				t.Errorf("%s (%s): rule verbs %v do not cover the spec summary's verb %q (summary: %q)",
					plugin, tag, verbs, summaryVerb, spec.Summary)
			}
		}
	}
}

// inflect must produce the spellings people write: e-drop, consonant
// doubling, -es. The lemma itself is always the first form.
func TestInflect(t *testing.T) {
	for _, tc := range []struct {
		lemma string
		forms []string
	}{
		{"remove", []string{"removes", "removed", "removing"}},
		{"strip", []string{"strips", "stripped", "stripping"}},
		{"drop", []string{"drops", "dropped", "dropping"}},
		{"cast", []string{"casts", "casting"}},
		{"add", []string{"adds", "added", "adding"}},
		{"unwrap", []string{"unwraps", "unwrapped", "unwrapping"}},
		{"encode", []string{"encodes", "encoded", "encoding"}},
		{"patch", []string{"patches", "patched", "patching"}},
	} {
		got := inflect(tc.lemma)
		if got[0] != tc.lemma {
			t.Errorf("inflect(%q)[0] = %q, want the lemma", tc.lemma, got[0])
		}
		for _, f := range tc.forms {
			if !slices.Contains(got, f) {
				t.Errorf("inflect(%q) = %v, missing %q", tc.lemma, got, f)
			}
		}
	}
}
