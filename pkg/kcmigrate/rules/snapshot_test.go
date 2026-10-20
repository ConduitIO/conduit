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
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/conduitio/conduit/pkg/kcmigrate"
)

// snapshotCase is a snapshot with the engine and class to run its keys in.
// Shared keys run in the test source class, whose class file has no rule for
// them, so the shared rules decide.
type snapshotCase struct {
	name   string
	snap   Snapshot
	engine *Engine
	class  string
	file   *File // the file that must hold the defaults entries
}

func snapshotCases(t *testing.T) []snapshotCase {
	t.Helper()
	paths, err := filepath.Glob("upstream/*.keys.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no upstream snapshots: %v", err)
	}
	var out []snapshotCase
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := ParseSnapshot(b)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		fsys := fstest.MapFS{}
		for _, name := range []string{"shared.yaml", "worker.yaml"} {
			b, err := os.ReadFile(filepath.Join("data", name))
			if err != nil {
				t.Fatal(err)
			}
			fsys[name] = &fstest.MapFile{Data: b}
		}
		class := snap.Class
		if snap.Scope == ScopeShared {
			b, err := os.ReadFile("testdata/valid/example-source.yaml")
			if err != nil {
				t.Fatal(err)
			}
			fsys["example.yaml"] = &fstest.MapFile{Data: b}
			class = exampleClass
		} else {
			matches, _ := filepath.Glob("data/*.yaml")
			for _, m := range matches {
				b, _ := os.ReadFile(m)
				fsys[filepath.Base(m)] = &fstest.MapFile{Data: b}
			}
		}
		e, err := Load(fsys)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		file := e.shared
		if snap.Scope != ScopeShared {
			file = e.classes[snap.Class]
			if file == nil {
				t.Fatalf("%s: no rule file for class %s", p, snap.Class)
			}
		}
		if file.Upstream != snap.Upstream {
			t.Errorf("%s: snapshot upstream %v, rule file %v", p, snap.Upstream, file.Upstream)
		}
		out = append(out, snapshotCase{name: p, snap: snap, engine: e, class: class, file: file})
	}
	return out
}

func classifyOne(sc snapshotCase, key, value string) kcmigrate.Entry {
	c := kcmigrate.Connector{Name: "c", Class: sc.class, Config: []kcmigrate.KV{{
		Key: key, Value: kcmigrate.NewValue(value), Origin: kcmigrate.Origin{File: "f", Line: 1, Column: 1},
	}}}
	return sc.engine.Connector(c).Entries[0]
}

// TestSnapshot_KeysAreKnown: every upstream key is claimed by a rule, so no
// documented key can be kc.key_unknown.
func TestSnapshot_KeysAreKnown(t *testing.T) {
	for _, sc := range snapshotCases(t) {
		for _, k := range sc.snap.Keys {
			if e := classifyOne(sc, k.Key, "x"); e.Code == kcmigrate.CodeKeyUnknown.Reason() {
				t.Errorf("%s: upstream key %s matches no rule", sc.name, k.Key)
			}
		}
	}
}

// TestSnapshot_AbsentDefaults is the mechanical absent-key test of the
// mapping design: an absent key behaves as its upstream default, so every
// default whose outcome is not plainly translated needs a defaults entry
// (a note, or coveredBy), and a defaults entry whose default is plainly
// translated is stale.
func TestSnapshot_AbsentDefaults(t *testing.T) {
	for _, sc := range snapshotCases(t) {
		entries := map[string]Default{}
		for _, d := range sc.file.Defaults {
			entries[d.Key] = d
		}
		for _, k := range sc.snap.Keys {
			if k.Default == nil {
				continue
			}
			e := classifyOne(sc, k.Key, *k.Default)
			plain := e.Status == kcmigrate.StatusTranslated && !e.Blocking
			d, has := entries[k.Key]
			switch {
			case !plain && !has:
				t.Errorf("%s: %s=%q (the upstream default) is %s (blocking %v) and %s has no defaults entry for it",
					sc.name, k.Key, *k.Default, e.Status, e.Blocking, sc.file.name)
			case plain && has:
				t.Errorf("%s: %s has a defaults entry, but its default %q is plainly translated: the entry is stale", sc.name, k.Key, *k.Default)
			case has && d.Default != *k.Default:
				t.Errorf("%s: defaults entry for %s says %q, the snapshot %q", sc.name, k.Key, d.Default, *k.Default)
			}
			delete(entries, k.Key)
		}
		for key := range entries {
			t.Errorf("%s: defaults entry %s is not an upstream key with a default", sc.name, key)
		}
	}
}
