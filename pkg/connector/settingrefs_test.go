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

// Stored-format tests for SettingRefs (design
// docs/design-documents/20261014-secrets-at-rest.md, implementation PR 1).
//
// testdata/v021_destination_instance.json is byte-identical to the fixture
// the v0.20.2 downgrade guard is tested against on release/v0.20.x (#3062).
// Writing exactly that shape is this release's half of the D9 contract: if
// these tests pass here and the guard's tests pass there, a v0.21 record is
// refused by v0.20.2 instead of running with settings missing. Do not edit
// the fixture without changing the release branch copy in the same way.
package connector

import (
	"context"
	"os"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

func v021DestinationInstance() *Instance {
	ts := time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)
	return &Instance{
		ID:   "v021-destination",
		Type: TypeDestination,
		Config: Config{
			Name:     "golden-destination-connector",
			Settings: map[string]string{"path": "/data/out"},
			SettingRefs: map[string]string{
				"aws.secretAccessKey": "${AWS_SECRET}",
				"sasl.password":       "$KAFKA_PASSWORD",
			},
		},
		PipelineID:   "golden-pipeline",
		Plugin:       "builtin:file",
		ProcessorIDs: []string{"proc-3"},
		State: DestinationState{
			Positions: map[string]opencdc.Position{
				"golden-source": opencdc.Position("golden-position-42"),
			},
		},
		ProvisionedBy: ProvisionTypeConfig,
		CreatedAt:     ts,
		UpdatedAt:     ts.Add(time.Minute),
		LastActiveConfig: Config{
			Name:        "golden-destination-connector",
			Settings:    map[string]string{"path": "/data/out"},
			SettingRefs: map[string]string{"aws.secretAccessKey": "${AWS_SECRET}"},
		},
	}
}

// TestSettingRefs_V021Fixture_ReadAndWrite: this release reads the record the
// downgrade guard is tested against, and writes exactly that record.
func TestSettingRefs_V021Fixture_ReadAndWrite(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	fixture, err := os.ReadFile("testdata/v021_destination_instance.json")
	is.NoErr(err)

	db := &inmemory.DB{}
	s := NewStore(db, log.Nop())
	is.NoErr(db.Set(ctx, s.addKeyPrefix("v021-destination"), fixture))

	got, err := s.Get(ctx, "v021-destination")
	is.NoErr(err)
	is.Equal(got, v021DestinationInstance())

	// Writer side: Set (the path the persister takes) produces the fixture.
	is.NoErr(s.Set(ctx, "written", v021DestinationInstance()))
	raw, err := db.Get(ctx, s.addKeyPrefix("written"))
	is.NoErr(err)
	assertJSONSemanticallyEqual(t, is, fixture, raw)
}

// TestSettingRefs_StoredKeyPinned freezes the encoded key name in both
// locations of a connector record. The v0.20.2 guard matches this key; a
// rename or a struct tag would disable it on every downgrade.
func TestSettingRefs_StoredKeyPinned(t *testing.T) {
	is := is.New(t)

	raw, err := (&Store{}).encode(v021DestinationInstance())
	is.NoErr(err)

	var rec map[string]any
	is.NoErr(json.Unmarshal(raw, &rec))
	for _, field := range []string{"Config", "LastActiveConfig"} {
		cfg, _ := rec[field].(map[string]any)
		refs, ok := cfg["SettingRefs"].(map[string]any)
		if !ok || len(refs) == 0 {
			t.Fatalf("%s: stored key must be exactly \"SettingRefs\" holding an object, got record %s", field, raw)
		}
	}
}

// TestSettingRefs_NoRefsOmitsKey: a record without references has no
// SettingRefs key, so its shape is v0.20's (the golden tests pin the rest).
func TestSettingRefs_NoRefsOmitsKey(t *testing.T) {
	for _, refs := range []map[string]string{nil, {}} {
		is := is.New(t)
		inst := v021DestinationInstance()
		inst.Config.SettingRefs = refs
		inst.LastActiveConfig.SettingRefs = refs

		raw, err := (&Store{}).encode(inst)
		is.NoErr(err)
		var generic map[string]any
		is.NoErr(json.Unmarshal(raw, &generic))
		for _, field := range []string{"Config", "LastActiveConfig"} {
			_, present := generic[field].(map[string]any)["SettingRefs"]
			is.True(!present)
		}
	}
}

// TestSettingRefs_V020ReaderIgnoresField documents the downgrade to v0.20.0
// and v0.20.1, which have no SettingRefs field and no guard: the record
// decodes without error and the reference-backed keys are absent. That is
// why D9 ships the v0.20.2 guard and why the release notes say not to
// downgrade to those versions without verifying the reconcile.
func TestSettingRefs_V020ReaderIgnoresField(t *testing.T) {
	is := is.New(t)

	// The connector record shape of v0.20.0/v0.20.1 (pkg/connector/instance.go
	// at v0.20.1): Config has Name and Settings only.
	type v020Config struct {
		Name     string
		Settings map[string]string
	}
	type v020Instance struct {
		ID               string
		Config           v020Config
		LastActiveConfig v020Config
	}

	raw, err := (&Store{}).encode(v021DestinationInstance())
	is.NoErr(err)

	var old v020Instance
	is.NoErr(json.Unmarshal(raw, &old))
	is.Equal(old.Config.Settings, map[string]string{"path": "/data/out"})
	_, hasSecret := old.Config.Settings["aws.secretAccessKey"]
	is.True(!hasSecret) // absent, not resolved and not the template
}

// TestSettingRefs_PersisterKeepsBothFields: the persister re-encodes the
// connector on every position flush through PrepareSet. Both maps must
// survive that, or the next start would see a connector without its
// reference-backed settings.
func TestSettingRefs_PersisterKeepsBothFields(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	persister, store := initPersisterTest(time.Hour, 1)

	conn := v021DestinationInstance()
	conn.ID = "persisted"
	done := make(chan error, 1)
	is.NoErr(persister.Persist(ctx, conn, func(err error) { done <- err }))
	persister.Flush(ctx)
	persister.Wait()
	select {
	case err := <-done:
		is.NoErr(err)
	case <-time.After(10 * time.Second):
		t.Fatal("persist callback was not called")
	}

	got, err := store.Get(ctx, "persisted")
	is.NoErr(err)
	is.Equal(got.Config.SettingRefs, conn.Config.SettingRefs)
	is.Equal(got.LastActiveConfig.SettingRefs, conn.LastActiveConfig.SettingRefs)
	is.Equal(got.State, conn.State) // Invariant 2: the position is untouched
}

// FuzzSettingRefs_StoreRoundTrip is the property decode(encode(c)) == c for
// a connector carrying both maps, over arbitrary keys and templates.
func FuzzSettingRefs_StoreRoundTrip(f *testing.F) {
	f.Add("path", "/data", "password", "${DB_PASSWORD}")
	f.Add("", "", "", "")
	f.Add("k", "<&>", "url", "postgres://u:${PW}@h/é")
	f.Fuzz(func(t *testing.T, sk, sv, rk, rv string) {
		ctx := context.Background()
		db := &inmemory.DB{}
		s := NewStore(db, log.Nop())

		want := &Instance{
			ID:   "fuzz",
			Type: TypeSource,
			Config: Config{
				Settings:    map[string]string{sk: sv},
				SettingRefs: map[string]string{rk: rv},
			},
			State: SourceState{Position: opencdc.Position("p")},
			LastActiveConfig: Config{
				SettingRefs: map[string]string{rk: rv},
			},
		}
		if err := s.Set(ctx, want.ID, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(ctx, want.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(rk) || !utf8.ValidString(rv) {
			// The JSON encoder replaces invalid UTF-8, as it does for
			// Settings; YAML input is always valid UTF-8.
			return
		}
		gotRefs := got.Config.SettingRefs[rk]
		if gotRefs != rv || got.LastActiveConfig.SettingRefs[rk] != rv {
			t.Fatalf("SettingRefs did not round-trip: want %q, got %q / %q",
				rv, gotRefs, got.LastActiveConfig.SettingRefs[rk])
		}
	})
}
