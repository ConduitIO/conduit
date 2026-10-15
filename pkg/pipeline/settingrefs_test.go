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

// Stored-format tests for pipeline DLQ.SettingRefs (design
// docs/design-documents/20261014-secrets-at-rest.md, implementation PR 1).
// testdata/v021_pipeline_instance.json is byte-identical to the fixture the
// v0.20.2 downgrade guard is tested against on release/v0.20.x (#3062); keep
// the two copies in sync.
package pipeline

import (
	"context"
	"os"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

// v021PipelineInstance is a pipeline whose DLQ carries a reference. Settings
// and plugin are irrelevant to the stored-key tests.
func v021PipelineInstance() *Instance {
	return &Instance{
		ID: "v021-pipeline",
		DLQ: DLQ{
			Plugin:      "builtin:file",
			Settings:    map[string]string{"path": "/dlq"},
			SettingRefs: map[string]string{"note": "${DLQ_NOTE}"},
		},
	}
}

func TestSettingRefs_V021Fixture_ReadAndWrite(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	fixture, err := os.ReadFile("testdata/v021_pipeline_instance.json")
	is.NoErr(err)

	db := &inmemory.DB{}
	s := NewStore(db)
	is.NoErr(db.Set(ctx, s.addKeyPrefix("v021-pipeline"), fixture))

	got, err := s.Get(ctx, "v021-pipeline")
	is.NoErr(err)
	is.Equal(len(got.DLQ.SettingRefs), 1)
	for _, tmpl := range got.DLQ.SettingRefs {
		is.Equal(tmpl, "${DLQ_MESSAGE}")
	}
	is.Equal(got.DLQ.WindowSize, 101)
	is.Equal(got.GetStatus(), StatusRunning)

	// Writer side: what this release writes for the decoded record is the
	// fixture, field for field.
	is.NoErr(s.Set(ctx, "v021-pipeline", got))
	raw, err := db.Get(ctx, s.addKeyPrefix("v021-pipeline"))
	is.NoErr(err)
	assertJSONSemanticallyEqual(t, is, fixture, raw)
}

// TestSettingRefs_StoredKeyPinned freezes the key the v0.20.2 guard matches.
func TestSettingRefs_StoredKeyPinned(t *testing.T) {
	is := is.New(t)
	raw, err := (&Store{}).encode(v021PipelineInstance())
	is.NoErr(err)

	var rec map[string]any
	is.NoErr(json.Unmarshal(raw, &rec))
	dlq, _ := rec["DLQ"].(map[string]any)
	refs, ok := dlq["SettingRefs"].(map[string]any)
	if !ok || len(refs) == 0 {
		t.Fatalf("stored key must be exactly \"SettingRefs\" holding an object, got record %s", raw)
	}
}

func TestSettingRefs_NoRefsOmitsKey(t *testing.T) {
	is := is.New(t)
	inst := v021PipelineInstance()
	inst.DLQ.SettingRefs = nil
	raw, err := (&Store{}).encode(inst)
	is.NoErr(err)

	var rec map[string]any
	is.NoErr(json.Unmarshal(raw, &rec))
	_, present := rec["DLQ"].(map[string]any)["SettingRefs"]
	is.True(!present)
}
