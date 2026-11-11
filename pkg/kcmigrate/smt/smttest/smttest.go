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

// Package smttest holds fixture SMT translators for testing the chain
// framework of package kcmigrate/smt. Their classes live under test.smt.,
// which no Kafka Connect deployment uses, and they are never registered in
// smt.Builtin: no real SMT is claimed supported by a fixture. The CLI and
// the MCP tool never see them; tests pass Registry as migrate.Request's
// Translators.
package smttest

import (
	"strings"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/smt"
)

// Fixture classes.
const (
	// Drop emits one filter processor, as the Filter translator does: with
	// a predicate it drops the matching records, without one every record.
	Drop = "test.smt.Drop"
	// Split emits one field.exclude per option a and b (both optional,
	// at least one), guarded against a nil side, so one SMT becomes
	// several processors whose entries point at their own processor.
	Split = "test.smt.Split"
	// Refuse refuses on its option mode, whatever its value, and translates
	// as a no-op without it.
	Refuse = "test.smt.Refuse"
	// Route appends -routed to the topic, and refuses when the topic is
	// unknown or the destination has no topic setting.
	Route = "test.smt.Route"

	// Wrap stands for a reshaping SMT that needs a known shape: it refuses
	// on a side whose converter carries a schema (decision 8) or whose shape
	// is unknown, and otherwise reshapes the side.
	Wrap = "test.smt.Wrap"
	// Unwrap is an unwrap point that writes no processor.
	Unwrap = "test.smt.Unwrap"
	// Clobber writes opencdc.collection, which the topic reads.
	Clobber = "test.smt.Clobber"

	// Always is a predicate that matches every record.
	Always = "test.smt.Always"
	// HasMeta is a predicate on a metadata key named by its option key.
	HasMeta = "test.smt.HasMeta"
	// Never is a predicate whose translator always refuses.
	Never = "test.smt.Never"
)

// Registry returns the fixture translators.
func Registry() *smt.Registry {
	return &smt.Registry{
		Transforms: map[string]smt.Translator{Drop: drop, Split: split, Refuse: refuse, Route: route, Unwrap: unwrap, Clobber: clobber, Wrap: wrap},
		Predicates: map[string]smt.PredicateTranslator{Always: always, HasMeta: hasMeta, Never: never},
	}
}

func drop(*smt.Chain, *smt.SMT) (smt.Result, error) {
	return smt.Result{Processors: []smt.Processor{{Plugin: "filter"}}}, nil
}

func split(_ *smt.Chain, t *smt.SMT) (smt.Result, error) {
	side := t.Side
	if side == smt.SideNone {
		side = smt.SideValue
	}
	var res smt.Result
	for _, name := range []string{"a", "b"} {
		v, ok := t.Option(name)
		if !ok {
			continue
		}
		res.Claimed = append(res.Claimed, name)
		field := side.Field() + "." + v
		res.Writes = append(res.Writes, smt.Path(field))
		res.Processors = append(res.Processors, smt.Processor{
			Plugin: fieldExclude, Settings: map[string]string{settingFields: field},
			Condition: smt.NilGuard(side), From: []string{name}, Reshapes: side,
		})
	}
	if len(res.Processors) == 0 {
		return smt.Result{Refusal: &smt.Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "Split needs a or b.", Fix: "Set a or b.",
		}}, nil
	}
	return res, nil
}

func refuse(_ *smt.Chain, t *smt.SMT) (smt.Result, error) {
	if _, ok := t.Option("mode"); ok {
		return smt.Result{Refusal: &smt.Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "Refuse refuses every mode.", Fix: "Remove mode.", Option: "mode",
		}}, nil
	}
	return smt.Result{Reason: "Refuse without mode changes nothing."}, nil
}

func route(c *smt.Chain, _ *smt.SMT) (smt.Result, error) {
	topic, ok := c.Topic()
	if !ok || c.Capability("destination").TopicSetting == "" {
		return smt.Result{Refusal: &smt.Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "The topic this SMT reads is not translated.", Fix: "Set the topic.",
		}}, nil
	}
	c.SetTopic(`print (` + topic + `) "-routed"`)
	return smt.Result{Reason: "Route rewrites the topic.", Reads: []smt.Path{collection}}, nil
}

const (
	fieldExclude  = "field.exclude"
	settingFields = "fields"
)

const collection = smt.Path(`.Metadata["opencdc.collection"]`)

func wrap(c *smt.Chain, t *smt.SMT) (smt.Result, error) {
	side := t.Side
	if side == smt.SideNone {
		side = smt.SideValue
	}
	switch {
	case c.SchemaCarrying(side):
		return smt.Result{Refusal: smt.SchemaRefusal()}, nil
	case c.Shape(side) == smt.ShapeUnknown:
		return smt.Result{Refusal: &smt.Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "Wrap needs a known shape.", Fix: "Use ByteArrayConverter.",
		}}, nil
	}
	return smt.Result{Processors: []smt.Processor{{
		Plugin: fieldExclude, Settings: map[string]string{settingFields: side.Field() + ".x"}, Reshapes: side,
	}}}, nil
}

func unwrap(*smt.Chain, *smt.SMT) (smt.Result, error) {
	return smt.Result{Unwraps: true, Reason: "Unwrap is a no-op here."}, nil
}

func clobber(*smt.Chain, *smt.SMT) (smt.Result, error) {
	return smt.Result{
		Processors: []smt.Processor{{Plugin: fieldExclude, Settings: map[string]string{settingFields: string(collection)}}},
		Writes:     []smt.Path{collection},
	}, nil
}

func always(*smt.Chain, *smt.Predicate) (smt.PredicateResult, error) {
	return smt.PredicateResult{Condition: "eq 1 1"}, nil
}

func hasMeta(_ *smt.Chain, p *smt.Predicate) (smt.PredicateResult, error) {
	k, ok := p.Option("key")
	if !ok || strings.ContainsAny(k, `"\`) {
		return smt.PredicateResult{Refusal: &smt.Refusal{
			Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "HasMeta needs a plain key.", Fix: "Set key.",
		}}, nil
	}
	return smt.PredicateResult{Condition: `hasKey .Metadata "` + k + `"`, Claimed: []string{"key"}}, nil
}

func never(*smt.Chain, *smt.Predicate) (smt.PredicateResult, error) {
	return smt.PredicateResult{Refusal: &smt.Refusal{
		Code: kcmigrate.CodeSMTUnsupported.Reason(), Reason: "Never is never translated.", Fix: "Remove it.",
	}}, nil
}
