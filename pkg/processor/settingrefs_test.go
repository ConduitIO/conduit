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

// Stored-format tests for processor Config.SettingRefs (design
// docs/design-documents/20261014-secrets-at-rest.md, implementation PR 1).
// testdata/v021_processor_instance.json is byte-identical to the fixture the
// v0.20.2 downgrade guard is tested against on release/v0.20.x (#3062); keep
// the two copies in sync.
package processor

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

func v021ProcessorInstance() *Instance {
	ts := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	return &Instance{
		ID:            "v021-processor",
		CreatedAt:     ts,
		UpdatedAt:     ts.Add(time.Minute),
		ProvisionedBy: ProvisionTypeConfig,
		Plugin:        "builtin:field.set",
		Condition:     `{{ eq .Metadata["key"] "value" }}`,
		Parent:        Parent{ID: "golden-pipeline", Type: ParentTypePipeline},
		Config: Config{
			Settings:    map[string]string{},
			Workers:     4,
			SettingRefs: map[string]string{"field": "${FIELD_VALUE}"},
		},
	}
}

func TestSettingRefs_V021Fixture_ReadAndWrite(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	fixture, err := os.ReadFile("testdata/v021_processor_instance.json")
	is.NoErr(err)

	db := &inmemory.DB{}
	s := NewStore(db)
	is.NoErr(db.Set(ctx, s.addKeyPrefix("v021-processor"), fixture))

	got, err := s.Get(ctx, "v021-processor")
	is.NoErr(err)
	is.Equal(got, v021ProcessorInstance())

	is.NoErr(s.Set(ctx, "written", v021ProcessorInstance()))
	raw, err := db.Get(ctx, s.addKeyPrefix("written"))
	is.NoErr(err)
	assertJSONSemanticallyEqual(t, is, fixture, raw)
}

// TestSettingRefs_StoredKeyPinned freezes the key the v0.20.2 guard matches.
func TestSettingRefs_StoredKeyPinned(t *testing.T) {
	is := is.New(t)
	raw, err := (&Store{}).encode(v021ProcessorInstance())
	is.NoErr(err)

	var rec map[string]any
	is.NoErr(json.Unmarshal(raw, &rec))
	cfg, _ := rec["Config"].(map[string]any)
	refs, ok := cfg["SettingRefs"].(map[string]any)
	if !ok || len(refs) == 0 {
		t.Fatalf("stored key must be exactly \"SettingRefs\" holding an object, got record %s", raw)
	}
}

func TestSettingRefs_NoRefsOmitsKey(t *testing.T) {
	is := is.New(t)
	inst := v021ProcessorInstance()
	inst.Config.SettingRefs = nil
	raw, err := (&Store{}).encode(inst)
	is.NoErr(err)

	var rec map[string]any
	is.NoErr(json.Unmarshal(raw, &rec))
	_, present := rec["Config"].(map[string]any)["SettingRefs"]
	is.True(!present)
}
