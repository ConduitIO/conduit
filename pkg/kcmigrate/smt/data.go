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

package smt

import (
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/kcmigrate"
)

// The translator's data is Go tables, not rule data (design, "Where the
// translator runs"): rules.Builtin and the migration harness read only rule
// files, and both would reject a file of another scope.

// Capability is what a Conduit plugin does, in one role, with the parts of a
// Kafka record that SMTs read or write, at the connector version pinned in
// go.mod. A capability is claimed only with a test that pins it; the
// translator that needs one sets it together with that test. Until then it
// is unset, so an SMT that reads or writes headers or partitions is refused
// (design decision 4).
type Capability struct {
	Plugin string
	Role   string // "source" or "destination"
	// HeaderPrefix is the metadata key prefix under which a source writes
	// Kafka headers, empty when it is not claimed.
	HeaderPrefix string
	// MetadataToHeaders: a destination that writes record metadata as Kafka
	// headers.
	MetadataToHeaders bool
	// ExplicitPartition: a destination that writes a partition a record
	// names.
	ExplicitPartition bool
	// TopicSetting is the setting that names a destination's target topic,
	// empty when it has none. The end of a chain writes its topic there; a
	// translator must refuse to change the topic of a chain whose
	// destination has none. TestCapabilities_ResolveAgainstPluginSpecs
	// checks it against the plugin's specification.
	TopicSetting string
}

// capabilities is the table: one row per plugin and role a rule file emits
// (TestCapabilities_CoverEmittedPlugins).
//
// Known and not yet claimed, for the translator PRs (checked in the
// connector sources at the go.mod versions):
//   - builtin:kafka source v0.17.0 writes header N as metadata
//     kafka.header.N (the last value wins) and the value as
//     opencdc.RawData(value): a null Kafka value is RawData(nil) and an
//     empty one RawData{}, which differ only in the nil-ness of the slice,
//     and that does not survive serialization. Only mirror mode records
//     kafka.value.null.
//   - builtin:kafka destination v0.17.0 outside mirror mode writes no
//     headers and partitions by key hash.
//   - builtin:s3 destination v0.13.0 names objects from opencdc.collection
//     only with a partitioner other than none, the default, and has no
//     topic setting.
//   - builtin:postgres source v0.20.0 sets opencdc.collection to the bare
//     relation name for CDC records (pglogrepl RelationName) but to the
//     entry of tables as configured for snapshot records, so a
//     schema-qualified tables entry (what table.include.list translates to)
//     gives public.users in the snapshot and users afterwards.
var capabilities = []Capability{
	{Plugin: "builtin:kafka", Role: roleSource},
	{Plugin: "builtin:kafka", Role: roleDestination, TopicSetting: "topic"},
	{Plugin: "builtin:postgres", Role: roleSource},
	{Plugin: "builtin:s3", Role: roleDestination},
}

const (
	roleSource      = "source"
	roleDestination = "destination"
)

// refusalGroup gives classes without a translator in this version their
// reason and fix, by class without the $Key/$Value suffix.
type refusalGroup struct {
	refusal Refusal
	classes []string
}

var refusalGroups = []refusalGroup{
	{
		refusal: Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "This version of migrate does not translate this transform or predicate yet. Transforms change data, so migrate does not approximate it.",
			Fix:    "Keep this connector on Kafka Connect until a migrate release translates it (SMT compatibility pack, #2960), or replace it by hand with the equivalent processor and remove it from the Kafka Connect config.",
		},
		classes: []string{
			"org.apache.kafka.connect.transforms.ExtractField",
			"org.apache.kafka.connect.transforms.Filter",
			"org.apache.kafka.connect.transforms.Flatten",
			"org.apache.kafka.connect.transforms.HoistField",
			"org.apache.kafka.connect.transforms.InsertField",
			"org.apache.kafka.connect.transforms.RegexRouter",
			"org.apache.kafka.connect.transforms.ReplaceField",
			"org.apache.kafka.connect.transforms.ValueToKey",
			"org.apache.kafka.connect.transforms.predicates.HasHeaderKey",
			"org.apache.kafka.connect.transforms.predicates.RecordIsTombstone",
			"org.apache.kafka.connect.transforms.predicates.TopicNameMatches",
			"io.debezium.transforms.ExtractNewRecordState",
		},
	},
	{
		refusal: Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "This transform is mapped in v0.23. Its exact translation needs type-aware conversion or header support that this version does not have, and transforms change data, so migrate does not approximate it.",
			Fix:    "Keep this connector on Kafka Connect until v0.23, or replace the transform by hand with processors and remove it from the Kafka Connect config.",
		},
		classes: []string{
			"org.apache.kafka.connect.transforms.Cast",
			"org.apache.kafka.connect.transforms.DropHeaders",
			"org.apache.kafka.connect.transforms.HeaderFrom",
			"org.apache.kafka.connect.transforms.InsertHeader",
			"org.apache.kafka.connect.transforms.TimestampConverter",
			"io.debezium.transforms.ExtractChangedRecordState",
			"io.debezium.transforms.TimezoneConverter",
			"io.debezium.transforms.outbox.EventRouter",
		},
	},
	{
		refusal: Refusal{
			Code:   kcmigrate.CodeTargetCapabilityMissing.Reason(),
			Reason: "The Kafka destination partitions by key only; explicit partition writing is v0.23 (per-partition ordering, invariant 4).",
			Fix:    "Keep this connector on Kafka Connect until v0.23.",
		},
		classes: []string{"io.debezium.transforms.partitions.PartitionRouting"},
	},
	{
		refusal: Refusal{
			Code:   kcmigrate.CodeSMTUnsupported.Reason(),
			Reason: "This transform is not mapped before v0.23.",
			Fix:    "Keep this connector on Kafka Connect, or write the equivalent processor with the processor SDK and remove the transform from the Kafka Connect config.",
		},
		classes: []string{
			"org.apache.kafka.connect.transforms.MaskField",
			"org.apache.kafka.connect.transforms.SetSchemaMetadata",
			"org.apache.kafka.connect.transforms.TimestampRouter",
			"io.debezium.transforms.ByLogicalTableRouter",
			"io.debezium.transforms.ContentBasedRouter",
			"io.debezium.transforms.Filter",
			"io.debezium.transforms.HeaderToValue",
		},
	},
}

// unknownRefusal is the refusal of every class without a row, custom
// classes included.
var unknownRefusal = Refusal{
	Code:   kcmigrate.CodeSMTUnsupported.Reason(),
	Reason: "migrate has no translation for this transform or predicate class; a custom class runs code migrate cannot read.",
	Fix:    "Keep this connector on Kafka Connect, or write the equivalent processor with the processor SDK and remove the transform from the Kafka Connect config.",
}

// Data is the translator's capability and refusal tables.
type Data struct {
	plugins  map[pluginRole]Capability
	refusals map[string]Refusal // class without suffix -> refusal
	unknown  Refusal
}

type pluginRole struct{ plugin, role string }

// BuiltinData returns the tables of this version. An error means a table
// is inconsistent, which TestBuiltinData catches before release.
func BuiltinData() (*Data, error) { return newData(capabilities, refusalGroups, unknownRefusal) }

func newData(caps []Capability, groups []refusalGroup, unknown Refusal) (*Data, error) {
	d := &Data{plugins: map[pluginRole]Capability{}, refusals: map[string]Refusal{}, unknown: unknown}
	var errs []error
	for _, c := range caps {
		k := pluginRole{c.Plugin, c.Role}
		switch {
		case c.Plugin == "" || (c.Role != roleSource && c.Role != roleDestination):
			errs = append(errs, cerrors.Errorf("capability %q needs a plugin and role source or destination", c.Plugin))
		case c.Role == roleSource && (c.MetadataToHeaders || c.ExplicitPartition || c.TopicSetting != ""):
			errs = append(errs, cerrors.Errorf("%s source: metadata-to-headers, explicit partition and topic setting are destination capabilities", c.Plugin))
		case c.Role == roleDestination && c.HeaderPrefix != "":
			errs = append(errs, cerrors.Errorf("%s destination: a header prefix is a source capability", c.Plugin))
		}
		if _, dup := d.plugins[k]; dup {
			errs = append(errs, cerrors.Errorf("%s %s has two capability rows", c.Plugin, c.Role))
		}
		d.plugins[k] = c
	}
	for _, g := range groups {
		if err := g.refusal.validate(); err != nil {
			errs = append(errs, err)
		}
		for _, c := range g.classes {
			if _, dup := d.refusals[c]; dup {
				errs = append(errs, cerrors.Errorf("class %s has two refusals", c))
			}
			if base, _ := splitClass(c); base != c {
				errs = append(errs, cerrors.Errorf("class %s: list it without its $Key/$Value suffix", c))
			}
			d.refusals[c] = g.refusal
		}
	}
	if err := unknown.validate(); err != nil {
		errs = append(errs, err)
	}
	if err := cerrors.Join(errs...); err != nil {
		return nil, err
	}
	return d, nil
}

// Capability returns the capabilities of plugin in role ("source" or
// "destination"). A plugin without a row claims none.
func (d *Data) Capability(plugin, role string) Capability {
	if c, ok := d.plugins[pluginRole{plugin, role}]; ok {
		return c
	}
	return Capability{Plugin: plugin, Role: role}
}

// HasCapability reports whether the table has a row for plugin in role.
func (d *Data) HasCapability(plugin, role string) bool {
	_, ok := d.plugins[pluginRole{plugin, role}]
	return ok
}

// Capabilities returns every row, sorted by plugin and role.
func (d *Data) Capabilities() []Capability {
	out := make([]Capability, 0, len(d.plugins))
	for _, c := range d.plugins {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Plugin < out[j].Plugin || (out[i].Plugin == out[j].Plugin && out[i].Role < out[j].Role)
	})
	return out
}

// refusal returns the refusal of a class without a translator.
func (d *Data) refusal(base string) Refusal {
	if r, ok := d.refusals[base]; ok {
		return r
	}
	return d.unknown
}

func (r Refusal) validate() error {
	if r.Reason == "" || r.Fix == "" || !strings.HasPrefix(r.Code, "kc.") {
		return cerrors.New("a refusal needs a kc.* code, a reason and a fix")
	}
	if _, ok := conduiterr.LookupCode(r.Code); !ok {
		return cerrors.Errorf("code %q is not registered", r.Code)
	}
	return nil
}
