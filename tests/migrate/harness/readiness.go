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

package harness

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/yaml/v3"
)

// RuleDataDir is where the CLI lane keeps one rule file per Kafka Connect
// class, plus the shared and worker files (pkg/kcmigrate/rules/data/README.md).
const RuleDataDir = "pkg/kcmigrate/rules/data"

// ClassDebeziumPostgres is the Debezium Postgres connector class.
const ClassDebeziumPostgres = "io.debezium.connector.postgresql.PostgresConnector"

// FamilyClasses maps each connector family to the Kafka Connect class its
// rule file declares.
var FamilyClasses = map[string]string{
	FamilyDebeziumPostgres: ClassDebeziumPostgres,
	"debezium-mysql":       "io.debezium.connector.mysql.MySqlConnector",
	"jdbc-source":          "io.confluent.connect.jdbc.JdbcSourceConnector",
	"jdbc-sink":            "io.confluent.connect.jdbc.JdbcSinkConnector",
	"s3-sink":              "io.confluent.connect.s3.S3SinkConnector",
	"elasticsearch-sink":   "io.confluent.connect.elasticsearch.ElasticsearchSinkConnector",
	"kafka-mirror":         "org.apache.kafka.connect.mirror.MirrorSourceConnector",
}

// RuleSet is what the rule data in the tree covers.
type RuleSet struct {
	Classes map[string]bool // classes with a rule file
	Shared  bool            // a scope: shared file exists
	Worker  bool            // a scope: worker file exists
}

// LoadRuleSet reads the scope and class of every rule file under
// repoRoot/RuleDataDir. A missing directory is an empty set. A class file
// whose class is not one of FamilyClasses is an error, not an ignored file:
// a typo (PostgresConector) would otherwise leave its family pending
// forever while the rule file looks landed. A new family adds its class to
// FamilyClasses in the PR that adds the rule file.
func LoadRuleSet(repoRoot string) (RuleSet, error) {
	rs := RuleSet{Classes: map[string]bool{}}
	files, err := filepath.Glob(filepath.Join(repoRoot, filepath.FromSlash(RuleDataDir), "*.yaml"))
	if err != nil {
		return rs, cerrors.Errorf("list rule data: %w", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return rs, cerrors.Errorf("read rule file: %w", err)
		}
		var head struct {
			Scope string `yaml:"scope"`
			Class string `yaml:"class"`
		}
		if err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&head); err != nil {
			return rs, cerrors.Errorf("%s: %w", filepath.Base(f), err)
		}
		base := filepath.Base(f)
		switch head.Scope {
		case "shared":
			rs.Shared = true
		case "worker":
			rs.Worker = true
		case roleSource, "sink":
			if !knownClass(head.Class) {
				return rs, cerrors.Errorf("%s: class %q is not a known corpus family class %v; fix the class or add the family to harness.FamilyClasses", base, head.Class, sortedClasses())
			}
			rs.Classes[head.Class] = true
		default:
			return rs, cerrors.Errorf("%s: scope %q is not source, sink, shared or worker", base, head.Scope)
		}
	}
	return rs, nil
}

func knownClass(c string) bool {
	for _, cl := range FamilyClasses {
		if cl == c {
			return true
		}
	}
	return false
}

func sortedClasses() []string {
	out := make([]string, 0, len(FamilyClasses))
	for _, cl := range FamilyClasses {
		out = append(out, cl)
	}
	sort.Strings(out)
	return out
}

// CaseReady reports whether the rule data covers everything a case
// exercises, which is when its checks stop being pending and become
// required. Each connector class in the input needs its rule file; a class
// outside the v0.21 families (the unsupported-class cases) needs the shared
// file; a --worker input needs the worker file. A case with no class in its
// input (a rejected input) uses its family's class. The reason names what is
// missing.
func CaseReady(c Case, inputs []InputFile, rs RuleSet) (bool, string) {
	classes := map[string]bool{}
	for _, f := range inputs {
		for _, k := range f.Keys {
			if k.Key == KeyConnectorClass && k.Value != "" {
				classes[k.Value] = true
			}
		}
	}
	if len(classes) == 0 {
		if cl, ok := FamilyClasses[c.Manifest.Family]; ok {
			classes[cl] = true
		}
	}
	known := map[string]bool{}
	for _, cl := range FamilyClasses {
		known[cl] = true
	}
	var missing []string
	for cl := range classes {
		switch {
		case known[cl] && !rs.Classes[cl]:
			missing = append(missing, "the "+cl+" rule file")
		case !known[cl] && !rs.Shared:
			missing = append(missing, "the shared rule file (for unsupported class "+cl+")")
		}
	}
	for i, a := range c.Manifest.Command.Args {
		if a == flagWorker && i+1 < len(c.Manifest.Command.Args) && !rs.Worker {
			missing = append(missing, "the worker rule file")
		}
	}
	if len(missing) == 0 {
		return true, ""
	}
	sort.Strings(missing)
	return false, RuleDataDir + " has no " + strings.Join(missing, ", no ")
}
