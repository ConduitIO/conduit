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

package smt_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/conduit/pkg/kcmigrate/schema"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt/smttest"
)

type kv struct{ k, v string }

// randomChain builds a connector config with a random transform chain:
// fixture and real classes, options, predicates (used, unused, undefined),
// negate, unlisted aliases, duplicates, references and CANARY values.
func randomChain(r *rand.Rand, i int) []kv {
	if r.IntN(2) == 0 {
		return cleanChain(r, i)
	}
	aliases := []string{"a", "b", "c", "a.b", "d_e"}
	classes := []string{
		smttest.Drop, smttest.Split, smttest.Split + "$Key", smttest.Refuse, smttest.Route,
		"org.apache.kafka.connect.transforms.Filter", "io.debezium.transforms.partitions.PartitionRouting", "com.example.Custom",
	}
	preds := []string{smttest.Always, smttest.HasMeta, smttest.Never, "com.example.CustomPredicate"}
	options := []string{"a", "b", "mode", "key", "x"}
	pick := func(s []string) string { return s[r.IntN(len(s))] }
	value := func() string {
		switch r.IntN(8) {
		case 0:
			return fmt.Sprintf("CANARY-%d", i)
		case 1:
			return "${file:/s.properties:k}"
		default:
			return pick([]string{"f", "g", "true", "false"})
		}
	}
	var out []kv
	var listed []string
	for range r.IntN(4) {
		listed = append(listed, pick(aliases))
	}
	if r.IntN(6) > 0 {
		out = append(out, kv{"transforms", strings.Join(listed, ",")})
	}
	var predList []string
	for range r.IntN(3) {
		predList = append(predList, pick([]string{"p", "q"}))
	}
	if r.IntN(3) > 0 {
		out = append(out, kv{"predicates", strings.Join(predList, ",")})
	}
	for _, al := range append(listed, pick(aliases)) {
		if r.IntN(8) > 0 {
			out = append(out, kv{"transforms." + al + ".type", pick(classes)})
		}
		for range r.IntN(3) {
			out = append(out, kv{"transforms." + al + "." + pick(options), value()})
		}
		if r.IntN(3) == 0 {
			out = append(out, kv{"transforms." + al + ".predicate", pick([]string{"p", "q", "z"})})
		}
		if r.IntN(4) == 0 {
			out = append(out, kv{"transforms." + al + ".negate", pick([]string{"true", "false", "maybe"})})
		}
	}
	for _, p := range append(predList, "q") {
		if r.IntN(6) > 0 {
			out = append(out, kv{"predicates." + p + ".type", pick(preds)})
		}
		if r.IntN(2) == 0 {
			out = append(out, kv{"predicates." + p + "." + pick(options), value()})
		}
	}
	return out
}

// cleanChain builds a chain every link of which the fixtures translate, so
// the translated side of the properties is exercised as much as the
// refused one.
func cleanChain(r *rand.Rand, i int) []kv {
	aliases := []string{"a", "b", "c", "a.b", "d_e"}
	r.Shuffle(len(aliases), func(x, y int) { aliases[x], aliases[y] = aliases[y], aliases[x] })
	listed := aliases[:1+r.IntN(4)]
	out := []kv{{"transforms", strings.Join(listed, ",")}, {"predicates", "p"}, {"predicates.p.type", smttest.HasMeta}, {"predicates.p.key", "k"}}
	for _, al := range listed {
		switch r.IntN(3) {
		case 0:
			out = append(out, kv{"transforms." + al + ".type", smttest.Drop})
		case 1:
			out = append(out, kv{"transforms." + al + ".type", smttest.Split + "$Key"}, kv{"transforms." + al + ".a", fmt.Sprintf("CANARY-%d", i)})
		default:
			out = append(out, kv{"transforms." + al + ".type", smttest.Split}, kv{"transforms." + al + ".b", "f"})
		}
		if r.IntN(2) == 0 {
			out = append(out, kv{"transforms." + al + ".predicate", "p"}, kv{"transforms." + al + ".negate", "true"})
		}
	}
	return out
}

func flat(kvs []kv) []string {
	out := make([]string, 0, 2*len(kvs))
	for _, p := range kvs {
		out = append(out, p.k, p.v)
	}
	return out
}

type outcome struct {
	status   kcmigrate.Status
	code     string
	blocking bool
	target   string
}

func outcomes(ds []rules.Decision) map[string]outcome {
	out := map[string]outcome{}
	for _, d := range ds {
		o := outcome{d.Entry.Status, d.Entry.Code, d.Entry.Blocking, ""}
		if d.Entry.Target != nil {
			o.target = d.Entry.Target.ConfigPath
		}
		out[d.KV.Key] = o // duplicates: only the last occurrence is chain-claimed
	}
	return out
}

// TestApply_Properties is the chain framework as properties, over random
// chains:
//   - completeness: every claimed key gets an outcome (Apply fails
//     otherwise), every entry satisfies the report contract and the
//     schema, and no value reaches the report;
//   - all or nothing: a blocking chain key means no processor;
//   - determinism: the same outcomes, processors and transform index for a
//     permuted key order, and the same processors modulo their IDs for
//     renamed aliases.
func TestApply_Properties(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	d, reg, env := data(t), smttest.Registry(), propertyEnv()
	var translated, refusedChains int
	for i := range 2000 {
		kvs := randomChain(r, i)
		ds := decisions(t, flat(kvs)...)
		out, err := smt.Apply(d, reg, env, ds)
		if err != nil {
			t.Fatalf("case %d: %v\n%v", i, err, kvs)
		}
		rep := kcmigrate.NewReport("test")
		rep.Connectors = append(rep.Connectors, kcmigrate.ReportConnector{Name: "c", Class: pgClass, Role: kcmigrate.RoleSource, Entries: rules.Entries(ds), Notes: []kcmigrate.Note{}, Transforms: out.Transforms})
		rep.Summarize()
		if err := rep.Validate(); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		b, err := kcmigrate.MarshalReport(rep)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "CANARY") {
			t.Fatalf("case %d: a value reached the report", i)
		}
		if i%100 == 0 {
			if err := schema.ValidateReport(b); err != nil {
				t.Fatalf("case %d: %v", i, err)
			}
		}
		if len(out.Processors) > 0 {
			translated++
		}
		for _, dd := range ds {
			if dd.KV.Key == "transforms" && dd.Entry.Blocking {
				refusedChains++
			}
			if dd.Entry.Blocking && len(out.Processors) > 0 {
				t.Fatalf("case %d: %s blocks but the chain wrote %d processors\n%v", i, dd.KV.Key, len(out.Processors), kvs)
			}
		}

		checkPermuted(t, r, i, kvs, ds, out)
		checkRenamed(t, i, kvs, out)
	}
	// The generator must reach both outcomes, or the properties prove little.
	if translated < 100 || refusedChains < 100 {
		t.Fatalf("generator too narrow: %d translated chains, %d refused", translated, refusedChains)
	}
}

// checkPermuted: the same outcome per key and the same processors for a
// permuted key order.
func checkPermuted(t *testing.T, r *rand.Rand, i int, kvs []kv, ds []rules.Decision, out smt.Outcome) {
	t.Helper()
	d, reg, env := data(t), smttest.Registry(), propertyEnv()
	// Permuted key order: same outcome per key, same processors.
	perm := append([]kv(nil), kvs...)
	r.Shuffle(len(perm), func(a, b int) { perm[a], perm[b] = perm[b], perm[a] })
	if !hasDuplicateKeys(kvs) {
		pds := decisions(t, flat(perm)...)
		pout, err := smt.Apply(d, reg, env, pds)
		if err != nil {
			t.Fatalf("case %d permuted: %v", i, err)
		}
		if fmt.Sprint(outcomes(pds)) != fmt.Sprint(outcomes(ds)) {
			t.Fatalf("case %d: outcomes depend on key order\n%v\n%v", i, kvs, perm)
		}
		if fmt.Sprint(pout) != fmt.Sprint(out) {
			t.Fatalf("case %d: processors depend on key order", i)
		}
	}
}

// checkRenamed: the same processors, modulo IDs, for renamed aliases.
func checkRenamed(t *testing.T, i int, kvs []kv, out smt.Outcome) {
	t.Helper()
	d, reg, env := data(t), smttest.Registry(), propertyEnv()
	// Renamed aliases: same processors modulo IDs.
	renamed := make([]kv, len(kvs))
	for j, p := range kvs {
		renamed[j] = kv{renameAliases(p.k), p.v}
		if p.k == "transforms" {
			renamed[j].v = renameList(p.v)
		}
	}
	rds := decisions(t, flat(renamed)...)
	rout, err := smt.Apply(d, reg, env, rds)
	if err != nil {
		t.Fatalf("case %d renamed: %v", i, err)
	}
	if len(rout.Processors) != len(out.Processors) {
		t.Fatalf("case %d: renaming aliases changed the processor count", i)
	}
	for j := range out.Processors {
		x, y := out.Processors[j], rout.Processors[j]
		x.ID, y.ID = "", ""
		if fmt.Sprint(x) != fmt.Sprint(y) {
			t.Fatalf("case %d: renaming aliases changed processor %d", i, j)
		}
	}
}

func propertyEnv() smt.Env {
	env := sourceEnv
	env.Topic = "orders"
	return env
}

func hasDuplicateKeys(kvs []kv) bool {
	seen := map[string]bool{}
	for _, p := range kvs {
		if seen[p.k] {
			return true
		}
		seen[p.k] = true
	}
	return false
}

// renameAliases maps every transform alias to another name that keeps the
// ownership relation (a.b stays longer than a).
var aliasRename = map[string]string{"a": "A", "b": "B", "c": "C", "a.b": "A.B", "d_e": "D_E"}

func renameAliases(key string) string {
	rest, ok := strings.CutPrefix(key, "transforms.")
	if !ok {
		return key
	}
	for _, al := range []string{"a.b", "d_e", "a", "b", "c"} { // longest first
		if strings.HasPrefix(rest, al+".") {
			return "transforms." + aliasRename[al] + rest[len(al):]
		}
	}
	return key
}

func renameList(v string) string {
	parts := strings.Split(v, ",")
	for i, p := range parts {
		if n, ok := aliasRename[p]; ok {
			parts[i] = n
		}
	}
	return strings.Join(parts, ",")
}
