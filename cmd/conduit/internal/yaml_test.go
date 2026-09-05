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

package internal_test

import (
	"bytes"
	"testing"

	"github.com/conduitio/conduit/cmd/conduit/internal"
	"github.com/conduitio/yaml/v3"
	"github.com/matryer/is"
)

func encode(t *testing.T, tree *internal.YAMLTree) string {
	t.Helper()
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(tree.Root); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.String()
}

// TestInsertSeq_EmptyRendersAsSequenceNotQuotedString pins the contract that
// `conduit init`'s generated conduit.yaml depends on: an empty list must
// round-trip as an empty sequence. Rendering it through fmt produced the
// quoted scalar '[]', which decodes back as a one-element []string{"[]"} and
// makes the generated config unstartable.
func TestInsertSeq_EmptyRendersAsSequenceNotQuotedString(t *testing.T) {
	is := is.New(t)
	tree := internal.NewYAMLTree()
	tree.InsertSeq("api.http.cors.allowed-origins", nil, "allowed origins")

	out := encode(t, tree)

	var decoded struct {
		API struct {
			HTTP struct {
				CORS struct {
					AllowedOrigins []string `yaml:"allowed-origins"`
				} `yaml:"cors"`
			} `yaml:"http"`
		} `yaml:"api"`
	}
	is.NoErr(yaml.Unmarshal([]byte(out), &decoded))
	is.Equal(len(decoded.API.HTTP.CORS.AllowedOrigins), 0)
}

// TestInsertSeq_NonEmptyRoundTrips covers the branch no config default
// exercises today: a populated list must survive encode/decode in order.
func TestInsertSeq_NonEmptyRoundTrips(t *testing.T) {
	is := is.New(t)
	tree := internal.NewYAMLTree()
	tree.InsertSeq("processors.egress.allow", []string{"api.openai.com", "https://api.voyageai.com:443"}, "allowlist")

	out := encode(t, tree)

	var decoded struct {
		Processors struct {
			Egress struct {
				Allow []string `yaml:"allow"`
			} `yaml:"egress"`
		} `yaml:"processors"`
	}
	is.NoErr(yaml.Unmarshal([]byte(out), &decoded))
	is.Equal(decoded.Processors.Egress.Allow, []string{"api.openai.com", "https://api.voyageai.com:443"})
}

// TestInsert_ScalarUnchanged guards the pre-existing scalar path through the
// shared insertNode refactor.
func TestInsert_ScalarUnchanged(t *testing.T) {
	is := is.New(t)
	tree := internal.NewYAMLTree()
	tree.Insert("db.type", "badger", "database type")
	tree.Insert("db.badger.path", "/var/lib/conduit", "")

	var decoded struct {
		DB struct {
			Type   string `yaml:"type"`
			Badger struct {
				Path string `yaml:"path"`
			} `yaml:"badger"`
		} `yaml:"db"`
	}
	is.NoErr(yaml.Unmarshal([]byte(encode(t, tree)), &decoded))
	is.Equal(decoded.DB.Type, "badger")
	is.Equal(decoded.DB.Badger.Path, "/var/lib/conduit")
}
