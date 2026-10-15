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

package rules

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/schema"
	"github.com/matryer/is"
)

const exampleClass = "com.example.ExampleSource"

func loadValid(t *testing.T) *Engine {
	t.Helper()
	e, err := Load(os.DirFS("testdata/valid"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return e
}

func connector(class string, kvs ...string) kcmigrate.Connector {
	c := kcmigrate.Connector{Name: "c1", Class: class, File: "c.properties"}
	for i := 0; i < len(kvs); i += 2 {
		c.Config = append(c.Config, kcmigrate.KV{
			Key: kvs[i], Value: kcmigrate.NewValue(kvs[i+1]),
			Origin: kcmigrate.Origin{File: "c.properties", Line: i/2 + 1, Column: 1},
		})
	}
	return c
}

type got struct {
	status   kcmigrate.Status
	code     string
	blocking bool
}

func TestEngine_Connector(t *testing.T) {
	e := loadValid(t)
	tests := []struct {
		name     string
		key, val string
		want     got
	}{
		{"exact translated", "example.host", "h", got{kcmigrate.StatusTranslated, "", false}},
		{"class rule shadows shared", "name", "c1", got{kcmigrate.StatusTranslated, "", false}},
		{"shared fallback", "tasks.max", "3", got{kcmigrate.StatusApproximated, "kc.key_approximated", false}},
		{"prefix", "example.tuning.linger", "1", got{kcmigrate.StatusUnsupported, "kc.key_unsupported", false}},
		{"longest prefix wins", "example.tuning.special.x", "1", got{kcmigrate.StatusApproximated, "kc.key_approximated", false}},
		{"regex", "example.mask.12", "x", got{kcmigrate.StatusUnsupported, "kc.key_unsupported", true}},
		{"regex does not match", "example.mask.x", "x", got{kcmigrate.StatusUnsupported, "kc.key_unknown", true}},
		{"unknown key", "example.hots", "h", got{kcmigrate.StatusUnsupported, "kc.key_unknown", true}},
		{"value branch", "example.mode", "fast", got{kcmigrate.StatusTranslated, "", false}},
		{"value branch ignores case", "example.mode", "SLOW", got{kcmigrate.StatusApproximated, "kc.key_approximated", false}},
		{"second value of a branch", "example.mode", "legacy-slow", got{kcmigrate.StatusApproximated, "kc.key_approximated", false}},
		{"unlisted value", "example.mode", "turbo", got{kcmigrate.StatusUnsupported, "kc.key_unsupported", true}},
		{"empty value", "example.mode", "", got{kcmigrate.StatusUnsupported, "kc.key_unsupported", true}},
		// Kafka Connect's ConfigDef.parseType trims string values, so a
		// trailing space does not change which value a key has.
		{"value with surrounding space", "example.mode", " fast \t", got{kcmigrate.StatusTranslated, "", false}},
		{"reference as value", "example.mode", "${file:/etc/x.properties:mode}", got{kcmigrate.StatusUnsupported, "kc.key_unsupported", true}},
		{"reference where value does not matter", "example.password", "${env:PW}", got{kcmigrate.StatusTranslated, "", false}},
		{"alias of translated", "example.old.host", "h", got{kcmigrate.StatusApproximated, "kc.key_deprecated", false}},
		{"alias keeps a worse outcome", "example.old.mode", "turbo", got{kcmigrate.StatusUnsupported, "kc.key_unsupported", true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			rc := e.Connector(connector(exampleClass, tt.key, tt.val))
			is.Equal(rc.Role, kcmigrate.RoleSource)
			is.Equal(len(rc.Entries), 1)
			ent := rc.Entries[0]
			is.Equal(ent.Key, tt.key)
			is.Equal(got{ent.Status, ent.Code, ent.Blocking}, tt.want)
			is.Equal(ent.Origin, kcmigrate.Origin{File: "c.properties", Line: 1, Column: 1})
			if ent.Status != kcmigrate.StatusTranslated {
				is.True(ent.Reason != "")
			}
			if ent.Blocking || ent.Status == kcmigrate.StatusUnsupported {
				is.True(ent.Fix != "")
			}
		})
	}
}

func TestEngine_AliasExplains(t *testing.T) {
	is := is.New(t)
	e := loadValid(t)
	ent := e.Connector(connector(exampleClass, "example.old.host", "h")).Entries[0]
	is.True(strings.Contains(ent.Reason, "legacy name of example.host"))
	is.Equal(ent.Fix, "Rename example.old.host to example.host.")
	ent = e.Connector(connector(exampleClass, "example.old.mode", "turbo")).Entries[0]
	is.True(strings.Contains(ent.Reason, "legacy name of example.mode"))
}

func TestEngine_Duplicates(t *testing.T) {
	is := is.New(t)
	e := loadValid(t)
	rc := e.Connector(connector(exampleClass, "example.mode", "turbo", "example.host", "h", "example.mode", "fast"))
	is.Equal(len(rc.Entries), 3)
	// The earlier occurrence is overridden, so its value has no effect.
	is.Equal(got{rc.Entries[0].Status, rc.Entries[0].Code, rc.Entries[0].Blocking}, got{kcmigrate.StatusTranslated, "kc.key_duplicate", false})
	is.True(strings.Contains(rc.Entries[0].Reason, "c.properties:3:1"))
	is.Equal(got{rc.Entries[2].Status, rc.Entries[2].Code, rc.Entries[2].Blocking}, got{kcmigrate.StatusTranslated, "", false})
}

func TestEngine_UnknownClass(t *testing.T) {
	is := is.New(t)
	e := loadValid(t)
	for _, class := range []string{"com.example.Nope", ""} {
		rc := e.Connector(connector(class, "connector.class", class, "tasks.max", "1", "tasks.max", "2"))
		is.Equal(rc.Role, kcmigrate.RoleUnknown)
		is.Equal(len(rc.Entries), 3)
		for _, ent := range rc.Entries {
			is.Equal(got{ent.Status, ent.Code, ent.Blocking}, got{kcmigrate.StatusUnsupported, "kc.connector_class_unsupported", true})
			is.True(ent.Fix != "")
		}
	}
}

func TestEngine_Worker(t *testing.T) {
	is := is.New(t)
	e := loadValid(t)
	w := kcmigrate.Worker{File: "connect.properties", Props: connector("", "group.id", "g", "tasks.max", "1").Config}
	_, rc := e.Worker(w)
	is.Equal(rc.Name, "connect.properties")
	is.Equal(rc.Role, kcmigrate.RoleWorker)
	is.Equal(rc.Entries[0].Status, kcmigrate.StatusTranslated)
	is.Equal(rc.Entries[1].Code, "kc.key_unknown") // shared connector rules do not apply to workers

	none, err := Load(fstest.MapFS{})
	is.NoErr(err)
	_, nrc := none.Worker(w)
	is.Equal(nrc.Entries[0].Code, "kc.key_unknown")
}

func TestEngine_Rules(t *testing.T) {
	is := is.New(t)
	is.Equal(loadValid(t).Rules(), []kcmigrate.RulesInfo{
		{Class: "(shared)", RulesVersion: 1, Upstream: kcmigrate.Upstream{Name: "kafka", Version: "4.2"}},
		{Class: "(worker)", RulesVersion: 1, Upstream: kcmigrate.Upstream{Name: "kafka", Version: "4.2"}},
		{Class: exampleClass, RulesVersion: 1, Upstream: kcmigrate.Upstream{Name: "example", Version: "1.0"}},
	})
}

// TestEngine_CompletenessProperty is the completeness invariant as a
// property: for random configs (known, unknown, duplicate keys, random
// values, unknown classes) the engine returns exactly one entry per KV, in
// order, with the KV's key and origin; every entry satisfies the report
// contract; and no value reaches the report. Adding an unknown key changes
// no existing entry.
func TestEngine_CompletenessProperty(t *testing.T) {
	e := loadValid(t)
	r := rand.New(rand.NewPCG(7, 11))
	keys := []string{
		"connector.class", "name", "tasks.max", "example.host", "example.password", "example.mode",
		"example.old.host", "example.old.mode", "example.tuning.a", "example.tuning.special.b",
		"example.mask.3", "example.unknown", "", "x.y",
	}
	values := []string{"fast", "slow", "SLOW", "turbo", "", "${env:X}"}
	classes := []string{exampleClass, exampleClass, exampleClass, "com.example.Nope"}
	for i := range 3000 {
		var kvs []kcmigrate.KV
		for j := range r.IntN(12) {
			v := values[r.IntN(len(values))]
			if r.IntN(3) == 0 {
				v = fmt.Sprintf("CANARY-%d-%d", i, j)
			}
			kvs = append(kvs, kcmigrate.KV{
				Key: keys[r.IntN(len(keys))], Value: kcmigrate.NewValue(v),
				Origin: kcmigrate.Origin{File: "f.json", Pointer: fmt.Sprintf("/config/k%d", j)},
			})
		}
		c := kcmigrate.Connector{Name: "c", Class: classes[r.IntN(len(classes))], Config: kvs}
		rc := e.Connector(c)
		if len(rc.Entries) != len(kvs) {
			t.Fatalf("case %d: %d entries for %d keys", i, len(rc.Entries), len(kvs))
		}
		for j, ent := range rc.Entries {
			if ent.Key != kvs[j].Key || ent.Origin != kvs[j].Origin {
				t.Fatalf("case %d entry %d: %q at %s, want %q at %s", i, j, ent.Key, ent.Origin, kvs[j].Key, kvs[j].Origin)
			}
		}

		rep := kcmigrate.NewReport("test")
		rep.Connectors = append(rep.Connectors, rc)
		rep.Summarize()
		if err := rep.Validate(); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		b, err := kcmigrate.MarshalReport(rep)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "CANARY-") {
			t.Fatalf("case %d: a value reached the report", i)
		}
		if i%100 == 0 {
			if err := schema.ValidateReport(b); err != nil {
				t.Fatalf("case %d: %v", i, err)
			}
		}

		// Monotone strictness: an extra unknown key only appends an entry.
		extra := append(append([]kcmigrate.KV(nil), kvs...), kcmigrate.KV{
			Key: fmt.Sprintf("zz.unknown.%d", i), Value: kcmigrate.NewValue("v"), Origin: kcmigrate.Origin{File: "f.json", Pointer: "/config/zz"},
		})
		rc2 := e.Connector(kcmigrate.Connector{Name: "c", Class: c.Class, Config: extra})
		for j := range rc.Entries {
			if fmt.Sprint(rc.Entries[j]) != fmt.Sprint(rc2.Entries[j]) {
				t.Fatalf("case %d: adding an unknown key changed entry %d", i, j)
			}
		}
		if last := rc2.Entries[len(rc2.Entries)-1]; !last.Blocking {
			t.Fatalf("case %d: unknown key not blocking", i)
		}
	}
}

func TestLoad_Rejects(t *testing.T) {
	const head = "rulesVersion: 1\nscope: source\nclass: C\nupstream: {name: u, version: \"1\"}\nkeys:\n"
	tests := []struct {
		name, yaml, want string
	}{
		{"unknown field", head + "  - key: a\n    status: translated\n    emit: {kind: none}\n    diff: identity\n    stauts: x\n", "stauts"},
		{"bad version", "rulesVersion: 2\nscope: shared\nupstream: {name: u, version: \"1\"}\n", "rulesVersion"},
		{"bad scope", "rulesVersion: 1\nscope: nope\nupstream: {name: u, version: \"1\"}\n", "unknown scope"},
		{"class missing", "rulesVersion: 1\nscope: sink\nupstream: {name: u, version: \"1\"}\n", "needs a class"},
		{"no upstream", "rulesVersion: 1\nscope: shared\n", "upstream"},
		{"translated without emit", head + "  - key: a\n    status: translated\n    diff: identity\n", "emit kind"},
		{"approximated without reason", head + "  - key: a\n    status: approximated\n    code: kc.key_approximated\n    diff: identity\n", "code and reason"},
		{"unsupported without fix", head + "  - key: a\n    status: unsupported\n    code: kc.key_unsupported\n    reason: r\n    diff: identity\n", "needs a fix"},
		{"blocking without fix", head + "  - key: a\n    status: translated\n    blocking: true\n    emit: {kind: none}\n    diff: identity\n", "needs a fix"},
		{"unregistered code", head + "  - key: a\n    status: approximated\n    code: kc.made_up\n    reason: r\n    diff: identity\n", "not a registered"},
		{"non-kc code", head + "  - key: a\n    status: approximated\n    code: internal.error\n    reason: r\n    diff: identity\n", "not a registered"},
		{"bad status", head + "  - key: a\n    status: dropped\n    diff: identity\n", "unknown status"},
		{"no diff", head + "  - key: a\n    status: translated\n    emit: {kind: none}\n", "diff must be"},
		{"two rules for a key", head + "  - key: a\n    status: translated\n    emit: {kind: none}\n    diff: identity\n  - key: a\n    status: translated\n    emit: {kind: none}\n    diff: identity\n", "two rules"},
		{"bad regex", head + "  - key: {regex: '('}\n    status: translated\n    emit: {kind: none}\n    diff: identity\n", "regex"},
		{"prefix and regex", head + "  - key: {prefix: a, regex: b}\n    status: translated\n    emit: {kind: none}\n    diff: identity\n", "exactly one of prefix, regex"},
		{"values without otherwise", head + "  - key: a\n    diff: identity\n    values:\n      - match: [x]\n        status: translated\n        emit: {kind: none}\n", "otherwise"},
		{"weak otherwise", head + "  - key: a\n    diff: identity\n    values:\n      - match: [x]\n        status: translated\n        emit: {kind: none}\n      - otherwise: true\n        status: unsupported\n        code: kc.key_unsupported\n        reason: r\n        fix: f\n", "otherwise branch must be"},
		{"value in two branches", head + "  - key: a\n    diff: identity\n    values:\n      - match: [x]\n        status: translated\n        emit: {kind: none}\n      - match: [x]\n        status: translated\n        emit: {kind: none}\n      - otherwise: true\n        status: unsupported\n        blocking: true\n        code: kc.key_unsupported\n        reason: r\n        fix: f\n", "two branches"},
		{"values and outcome", head + "  - key: a\n    status: translated\n    diff: identity\n    values:\n      - otherwise: true\n        status: unsupported\n        blocking: true\n        code: kc.key_unsupported\n        reason: r\n        fix: f\n", "outcome per value"},
		{"dangling alias", head + "  - key: a\n    aliasOf: b\n    diff: identity\n", "aliasOf"},
		{"alias with outcome", head + "  - key: b\n    status: translated\n    emit: {kind: none}\n    diff: identity\n  - key: a\n    aliasOf: b\n    status: translated\n    diff: identity\n", "outcome from the aliased key"},
		{"default without note", head + "  - key: a\n    status: translated\n    emit: {kind: none}\n    diff: identity\ndefaults:\n  - key: a\n    default: x\n", "exactly one of note, coveredBy"},
		{"default with both", head + "defaults:\n  - key: a\n    coveredBy: kc.default_differs\n    note: {code: kc.default_differs, reason: r}\n", "exactly one of note, coveredBy"},
		{"dangling coveredBy", head + "defaults:\n  - key: a\n    coveredBy: kc.default_differs\n", "not the code of a note"},
		{"unregistered note code", head + "defaults:\n  - key: a\n    note: {code: kc.made_up, reason: r}\n", "registered kc.* code"},
		{"blocking note without fix", head + "defaults:\n  - key: a\n    note: {code: kc.default_differs, reason: r, blocking: true}\n", "a fix when blocking"},
		{"overlapping regexes", head + "  - key: {regex: '^a\\.'}\n    status: translated\n    emit: {kind: none}\n    diff: identity\n  - key: {regex: '^a\\.b'}\n    status: translated\n    emit: {kind: none}\n    diff: identity\n", "more than one regex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			_, err := Load(fstest.MapFS{"r.yaml": {Data: []byte(tt.yaml)}})
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), tt.want)) // the error names the problem
		})
	}
}

func TestLoad_RejectsTwoFilesForOneScope(t *testing.T) {
	is := is.New(t)
	shared := []byte("rulesVersion: 1\nscope: shared\nupstream: {name: u, version: \"1\"}\n")
	_, err := Load(fstest.MapFS{"a.yaml": {Data: shared}, "b.yaml": {Data: shared}})
	is.True(err != nil && strings.Contains(err.Error(), "both have scope shared"))

	class := []byte("rulesVersion: 1\nscope: sink\nclass: C\nupstream: {name: u, version: \"1\"}\n")
	_, err = Load(fstest.MapFS{"a.yaml": {Data: class}, "b.yaml": {Data: class}})
	is.True(err != nil && strings.Contains(err.Error(), "both have class C"))
}
