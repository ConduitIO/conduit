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

package schemaregistry

import (
	"testing"

	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

func goldenCorpus() []sr.Schema {
	return []sr.Schema{
		{Schema: "a"},
		{Schema: "a", Type: sr.TypeJSON},
		{Schema: "a", SchemaMetadata: &sr.SchemaMetadata{}},
		{Schema: "a", SchemaMetadata: &sr.SchemaMetadata{Properties: map[string]string{"z": "1", "a": "2", "m": "<>& é\"\\\n"}}},
		{Schema: "a", SchemaMetadata: &sr.SchemaMetadata{Tags: map[string][]string{"b": {"x", "y"}, "a": nil, "c": {}}, Sensitive: []string{"s<"}}},
		{Schema: "a", SchemaRuleSet: &sr.SchemaRuleSet{}},
		{Schema: "a", SchemaRuleSet: &sr.SchemaRuleSet{DomainRules: []sr.SchemaRule{{Name: "r", Expr: "a<b && c>d", Doc: "é ", Params: map[string]string{"k2": "v", "k1": "w"}, Disabled: true}}}},
		{Schema: "a", SchemaRuleSet: &sr.SchemaRuleSet{MigrationRules: []sr.SchemaRule{{Name: "m", Tags: []string{}}, {Name: "n", Tags: []string{"t"}}}}},
		{Schema: "a", References: []sr.SchemaReference{{Name: "b", Subject: "s", Version: 2}, {Name: "a", Subject: "s", Version: 1}}},
	}
}

// TestNewSubjectDigest_Golden pins the cache key derivation. The expected
// values were produced before the JSON encoder was switched to
// github.com/goccy/go-json, so a change in bytes (and therefore in keys) shows
// up here.
func TestNewSubjectDigest_Golden(t *testing.T) {
	want := []subjectDigest{
		"29a99088e57cab4b3c755a7e1089ca37debef9624bbac0da5c01e643771edf6c",
		"ea69ef10143488821f4ea64df563b451e62bb9a9a03e8cb4ced06b3ee7208630",
		"29a99088e57cab4b3c755a7e1089ca37debef9624bbac0da5c01e643771edf6c",
		"011590b29755b69f54919338df937a3706b3015cd882aeeaf047cc8f34b5346e",
		"362a68ad93758cf9be0ff01e1f47834b28318d382cbbc58933d946d826e69cf9",
		"29a99088e57cab4b3c755a7e1089ca37debef9624bbac0da5c01e643771edf6c",
		"4f10287dcda467b9bacdf3c7ac04f837feb589fab289ce315eef9d61d5f7103a",
		"fa1a7055f074933aa4d81f53144cfb87d328c966969a81b53c380a44f6560085",
		"4577591660833a2a59ef14285639c99cc3c566ad0fb1e2fef204a016fa23484f",
	}
	corpus := goldenCorpus()
	is := is.New(t)
	is.Equal(len(corpus), len(want))
	for i, s := range corpus {
		is.Equal(newSubjectDigest("subj", s), want[i])
	}
}
