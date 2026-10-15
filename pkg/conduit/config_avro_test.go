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

package conduit

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/conduitio/conduit-commons/schema/avro"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/matryer/is"
)

func TestDefaultConfig_AvroMaxElements(t *testing.T) {
	is := is.New(t)
	is.Equal(DefaultConfig().Schema.Avro.MaxElements, 1_000_000)
}

func TestConfig_Validate_AvroMaxElements(t *testing.T) {
	for _, n := range []int{0, 1, 1_000_000, 2_000_000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			is := is.New(t)
			cfg := DefaultConfig()
			cfg.DB.Driver = nil
			cfg.DB.Type = DBTypeInMemory
			cfg.Schema.Avro.MaxElements = n
			is.NoErr(cfg.Validate())
		})
	}

	t.Run("negative is a coded error naming the setting", func(t *testing.T) {
		is := is.New(t)
		cfg := DefaultConfig()
		cfg.DB.Type = DBTypeInMemory
		cfg.Schema.Avro.MaxElements = -1
		err := cfg.Validate()
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
		is.Equal(ce.ConfigPath, "schema.avro.max-elements")
	})
}

// TestNewRuntime_AppliesAvroMaxElements: the runtime applies
// schema.avro.max-elements process-wide before any pipeline starts.
// Not parallel: the limit is process-global.
func TestNewRuntime_AppliesAvroMaxElements(t *testing.T) {
	// 1,000,001 null elements: zero bytes each, so the payload is tiny.
	payload := append(binary.AppendUvarint(nil, 2*1_000_001), 0)
	decode := func(name string) error {
		srd, err := avro.Parse([]byte(`{"type":"record","name":"` + name + `","fields":[{"name":"a","type":{"type":"array","items":"null"}}]}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		var v map[string]any
		return srd.Unmarshal(payload, &v)
	}
	t.Cleanup(func() {
		if err := schemaregistry.ApplyAvroMaxElements(schemaregistry.DefaultAvroMaxElements); err != nil {
			t.Errorf("restore: %v", err)
		}
	})

	newRuntime := func(n int) {
		t.Helper()
		cfg := DefaultConfig()
		cfg.DB.Type = DBTypeInMemory
		cfg.API.Enabled = false
		cfg.Pipelines.Path = t.TempDir()
		cfg.Schema.Avro.MaxElements = n
		r, err := NewRuntime(cfg)
		if err != nil {
			t.Fatalf("new runtime: %v", err)
		}
		_ = r.DB.Close()
	}

	is := is.New(t)
	newRuntime(1_000_000)
	is.True(decode("rtdefault") != nil) // default rejects 1,000,001

	newRuntime(2_000_000)
	is.NoErr(decode("rtraised"))

	newRuntime(0)
	is.NoErr(decode("rtunlimited"))
}
