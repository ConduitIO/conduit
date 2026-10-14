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

package connector

// Fixture of a record as Conduit v0.21 writes it (design
// 20261014-secrets-at-rest.md): reference-backed settings are stored as
// ${VAR} templates under a key named exactly SettingRefs, a map[string]string,
// and are absent from Settings. That key name and type are a compatibility
// contract between v0.21 and the v0.20.2 downgrade guard (decision D9). This
// release must decode the field, however the key is cased, and write it back
// (Invariant 6), so the lifecycle can refuse to run the pipeline.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestV021Record_SettingRefsDecodedAndRewritten(t *testing.T) {
	for _, key := range []string{"SettingRefs", "settingrefs", "sEtTiNgReFs"} {
		t.Run(key, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			golden, err := os.ReadFile("testdata/v021_destination_instance.json")
			is.NoErr(err)
			stored := []byte(strings.ReplaceAll(string(golden), "SettingRefs", key))

			db := &inmemory.DB{}
			s := NewStore(db, log.Nop())
			is.NoErr(db.Set(ctx, s.addKeyPrefix("v021-destination"), stored))

			got, err := s.Get(ctx, "v021-destination")
			is.NoErr(err)
			is.True(got.Config.HasSettingRefs())
			is.Equal(got.Config.SettingRefs, map[string]string{"aws.secretAccessKey": "${AWS_SECRET}", "sasl.password": "$KAFKA_PASSWORD"})
			is.Equal(got.Config.Settings, map[string]string{"path": "/data/out"})
			is.Equal(got.LastActiveConfig.SettingRefs, map[string]string{"aws.secretAccessKey": "${AWS_SECRET}"})

			// GetAll decodes it too.
			all, err := s.GetAll(ctx)
			is.NoErr(err)
			is.Equal(len(all), 1)

			// A re-write (state, position or status flush) keeps the field.
			is.NoErr(s.Set(ctx, "v021-destination", got))
			raw, err := db.Get(ctx, s.addKeyPrefix("v021-destination"))
			is.NoErr(err)
			assertJSONSemanticallyEqual(t, is, golden, raw)
		})
	}
}
