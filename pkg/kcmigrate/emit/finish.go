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

package emit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// Finisher is a class-level action run once per pipeline after every key
// and worker setting is applied, for a setting that depends on several keys
// and their upstream defaults (Plan.Config holds the connector's values).
// It returns connector-level notes for what it could not write.
type Finisher func(p *Plan, a rules.Action) ([]kcmigrate.Note, error)

// Finisher names.
const (
	// FinishMM2Topic names the MirrorMaker topic-naming finisher.
	FinishMM2Topic = "mm2-topic"
	// FinishDefault writes a setting no key set, for an upstream default
	// that Conduit's default differs from. Arguments: side, name, value.
	FinishDefault = "default-setting"
	// FinishMM2Offsets names the finisher of MirrorMaker's
	// kc.offsets_not_migrated note, which says where the pipeline starts.
	FinishMM2Offsets = "mm2-offsets"
	// FinishRequireKey blocks when the connector config lacks a key the
	// upstream connector requires (no default). Argument: key.
	FinishRequireKey = "require-key"
	// FinishSinkConsumer writes the consumer group and start position of
	// Kafka Connect's own consumer of a sink connector, where no key or
	// worker setting does: group.id=connect-<name> and
	// auto.offset.reset=earliest (Worker.baseConsumerConfigs). Takes no
	// arguments.
	FinishSinkConsumer = "sink-consumer-defaults"
	// FinishPGURL names the finisher that assembles the Postgres connection
	// URL from the parts the pg-url kind recorded (host, port, user,
	// password, dbname and the ssl* parts), into the source's url setting.
	FinishPGURL = "pg-url"
)

var finishers = map[string]Finisher{
	FinishMM2Topic:   mm2Topic,
	FinishDefault:    defaultSetting,
	FinishMM2Offsets: mm2Offsets,
	FinishRequireKey: requireKey,
	FinishPGURL:      pgURL,

	FinishSinkConsumer: sinkConsumerDefaults,
}

// requireKey returns a blocking kc.key_required note when the config does
// not set the key: the upstream connector has no default for it, so Kafka
// Connect rejects the config, and nothing migrate writes could stand for
// the value the running connector uses.
func requireKey(p *Plan, a rules.Action) ([]kcmigrate.Note, error) {
	key := a.Args["key"]
	if key == "" {
		return nil, cerrors.Errorf(FinishRequireKey + " needs a key")
	}
	if _, ok := p.config[key]; ok {
		return nil, nil
	}
	return []kcmigrate.Note{{
		Code: kcmigrate.CodeKeyRequired.Reason(), Blocking: true, Rule: FinishRequireKey + "/" + key,
		Reason: "The connector requires " + key + ", which has no upstream default, and the config does not set it; Kafka Connect rejects this config, so it is not the one the running connector uses.",
		Fix:    "Set " + key + " to the value of the running connector (GET /connectors/<name>/config), then re-run.",
	}}, nil
}

// mm2Offsets returns the blocking kc.offsets_not_migrated note. MirrorMaker
// resumes every partition from the offsets Kafka Connect stored, which
// migrate does not carry over, so the note states the start position the
// pipeline was actually written with. Run it after every finisher that may
// write readFromBeginning.
func mm2Offsets(p *Plan, _ rules.Action) ([]kcmigrate.Note, error) {
	const head = "MirrorMaker resumes every partition from the offsets Kafka Connect stored for it, and reads a partition with none from the beginning unless auto.offset.reset says otherwise. migrate does not carry the stored offsets over. "
	var start string
	src := p.connector(SideSource)
	switch v, ok := src.Settings["readFromBeginning"]; {
	case ok && v == tlsOn:
		start = "The pipeline reads every partition from the beginning (readFromBeginning: true), so the records MirrorMaker already copied are copied again."
	case ok && v == "false":
		start = "The pipeline starts every partition at the end (readFromBeginning: false, from auto.offset.reset=latest), so the records written before it starts are skipped."
	default:
		start = "readFromBeginning is not written (a blocked auto.offset.reset owns it), so the Kafka source's default applies: it starts every partition at the end, and the records written before it starts are skipped."
	}
	return []kcmigrate.Note{{
		Code: kcmigrate.CodeOffsetsNotMigrated.Reason(), Blocking: true, Rule: FinishMM2Offsets,
		Reason: head + start,
		Fix:    "Stop the MirrorMaker connector before starting the pipeline, and accept the re-copied or skipped records with --accept-gaps, which records kc.offsets_not_migrated in acceptedGaps.",
	}}, nil
}

// Settings of the builtin:kafka source the sink-consumer-defaults finisher
// writes. A test resolves them against the plugin's specification.
const (
	settingGroupID           = "groupID"
	settingReadFromBeginning = "readFromBeginning"
)

// SinkConsumerSettings lists the builtin:kafka source settings the
// sink-consumer-defaults finisher writes.
func SinkConsumerSettings() []string {
	return []string{settingGroupID, settingReadFromBeginning}
}

// sinkConsumerGroup is the consumer group Kafka Connect gives a sink
// connector's consumer unless the worker's consumer.group.id or the
// connector's consumer.override.group.id replaces it
// (SinkUtils.consumerGroupId, Apache Kafka 4.2.0).
func sinkConsumerGroup(connector string) string { return "connect-" + connector }

// sinkConsumerDefaults carries Kafka Connect's own consumer defaults into a
// sink's Kafka source. Kafka Connect reads a sink's topics in the group
// connect-<name> and starts a partition with no committed offset at the
// beginning (auto.offset.reset=earliest); the Kafka source defaults to a new
// random group and to the end of each partition. Left alone, a migrated sink
// would ignore the offsets the sink committed and skip every record written
// before the pipeline starts.
//
// Invariant 3: a migrated sink resumes from the group's committed offsets
// and, for a partition without one, from the beginning; it never starts at
// the end because a default differs.
//
// The defaults are written below every worker and connector layer, so a
// setting a key or the worker file set, or a blocked key claimed, is left
// as it is. It returns a note for each default it wrote (kc.default_differs,
// since the absent key has no entry to carry the difference) and the
// cutover note, which holds whichever way the group was chosen.
func sinkConsumerDefaults(p *Plan, _ rules.Action) ([]kcmigrate.Note, error) {
	idx, err := p.sides(SideConsumer)
	if err != nil {
		return nil, cerrors.Errorf(FinishSinkConsumer+": %w", err)
	}
	group := sinkConsumerGroup(p.ID)
	var notes []kcmigrate.Note
	p.origin, p.layer = OriginConnector, LayerFallback
	for _, d := range []struct{ name, value, key, reason string }{
		{
			settingGroupID, group, "(default consumer group.id)",
			"Kafka Connect reads a sink's topics in the consumer group connect-<name>, and the Kafka source defaults to a new random group. groupID is set to " + group + ", so the pipeline resumes from the offsets the Kafka Connect sink committed to that group.",
		},
		{
			settingReadFromBeginning, "true", "(default consumer auto.offset.reset)",
			"Kafka Connect's consumer defaults auto.offset.reset to earliest, and the Kafka source defaults to the end of each partition. readFromBeginning is set to true: a partition the group has no committed offset for is read from its first record, as Kafka Connect does, and a partition with a committed offset resumes from it.",
		},
	} {
		_, overriddenBy, err := p.Set(SideConsumer, d.name, d.value, d.key)
		if err != nil {
			return nil, cerrors.Errorf(FinishSinkConsumer+": %w", err)
		}
		if overriddenBy != "" {
			continue // a key or the worker decided it; its entry says how
		}
		notes = append(notes, kcmigrate.Note{
			Code: kcmigrate.CodeDefaultDiffers.Reason(), Rule: FinishSinkConsumer + "/" + d.name,
			Reason: d.reason,
		})
	}
	// Read back what the pipeline has: the group may come from a key.
	actual, ok := p.connectors[idx[0]].Settings[settingGroupID]
	if !ok {
		return notes, nil // a blocked key owns groupID; its entry blocks the pipeline
	}
	return append(notes, kcmigrate.Note{
		Code: kcmigrate.CodeConsumerGroupShared.Reason(), Rule: FinishSinkConsumer + "/cutover",
		Reason: "The pipeline's Kafka source reads in the consumer group " + actual + ", the group the Kafka Connect sink commits its offsets to, and resumes from them. While the Kafka Connect connector is still running, both consumers are in the group and Kafka splits the partitions between them, so neither sees every record. Records the sink processed after its last committed offset are read again, so they can reach the destination twice.",
		Fix:    "Stop the Kafka Connect connector (PUT /connectors/<name>/stop, or delete it; pausing keeps its group membership) and wait until kafka-consumer-groups --describe --group " + actual + " lists no members, then start the pipeline.",
	}), nil
}

// pgURL assembles the Postgres connection URL from the parts the pg-url kind
// recorded (see pgURLPart) and writes it to the source's url setting. It
// writes nothing when no part was recorded, so the required-settings check
// reports the missing url as kc.setting_required. A secret part is already a
// ${VAR} reference, so the assembled URL carries no plaintext secret.
func pgURL(p *Plan, _ rules.Action) ([]kcmigrate.Note, error) {
	if len(p.pgURL) == 0 {
		return nil, nil
	}
	var parts []string
	for _, name := range pgURLParts {
		if v := p.pgURL[name]; v != "" {
			parts = append(parts, name+"="+dsnQuote(v))
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	if _, _, err := p.Set(SideSource, "url", strings.Join(parts, " "), "(pg-url)"); err != nil {
		return nil, cerrors.Errorf(FinishPGURL+": %w", err)
	}
	return nil, nil
}

// dsnQuote single-quotes a libpq keyword/value value that needs it: a value
// with whitespace, a single quote or a backslash. libpq reads an unquoted
// value up to the next whitespace.
func dsnQuote(v string) string {
	if !strings.ContainsAny(v, " \t'\\") {
		return v
	}
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

// defaultSetting writes value to setting name of side unless a key or the
// worker set it: the upstream connector's own default, where Conduit's
// differs (MirrorMaker reads a partition with no stored offset from the
// beginning; the Kafka source starts at the end).
func defaultSetting(p *Plan, a rules.Action) ([]kcmigrate.Note, error) {
	side, name, value := a.Args["side"], a.Args["name"], a.Args["value"]
	if name == "" || value == "" {
		return nil, cerrors.Errorf(FinishDefault + " needs a name and a value")
	}
	idx, err := p.sides(side)
	if err != nil {
		return nil, cerrors.Errorf(FinishDefault+": %w", err)
	}
	for _, i := range idx {
		if _, ok := p.connectors[i].Settings[name]; ok {
			return nil, nil
		}
	}
	if _, _, err := p.Set(side, name, value, "(default "+side+" "+name+")"); err != nil {
		return nil, cerrors.Errorf(FinishDefault+": %w", err)
	}
	return nil, nil
}

// SetConfig gives the plan the connector's config, the last value of each
// key, for finishers. Values are only read by finishers, never copied into
// a report.
func (p *Plan) SetConfig(kvs []kcmigrate.KV) {
	p.config = map[string]string{}
	for _, kv := range kvs {
		p.config[kv.Key] = strings.TrimSpace(kv.Value.Reveal())
	}
}

// Finish runs the class's finish actions.
func Finish(p *Plan, actions []rules.Action) ([]kcmigrate.Note, error) {
	var notes []kcmigrate.Note
	for _, a := range actions {
		f, ok := finishers[a.Kind]
		if !ok {
			return nil, cerrors.Errorf("unknown finish kind %q", a.Kind)
		}
		p.origin, p.layer = OriginConnector, LayerConnector
		n, err := f(p, a)
		if err != nil {
			return nil, err
		}
		notes = append(notes, n...)
	}
	return notes, nil
}

// FinishKinds returns the registered finisher names, sorted.
func FinishKinds() []string {
	out := make([]string, 0, len(finishers))
	for k := range finishers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const collectionTemplate = `{{ index .Metadata "opencdc.collection" }}`

// MirrorMaker 2 replication policies (connect/mirror-client, Apache Kafka
// 4.2.0) and their defaults (MirrorClientConfig, MirrorConnectorConfig).
const (
	mm2DefaultPolicy  = "org.apache.kafka.connect.mirror.DefaultReplicationPolicy"
	mm2IdentityPolicy = "org.apache.kafka.connect.mirror.IdentityReplicationPolicy"
	mm2DefaultSep     = "."
)

// mm2Topic writes the destination topic template that reproduces the
// replication policy's remote topic name: DefaultReplicationPolicy names a
// topic <source.cluster.alias><separator><topic>, IdentityReplicationPolicy
// keeps the name. The Kafka source sets opencdc.collection to the source
// topic. Any other policy is blocking on its own entry, so nothing is
// written for it here.
func mm2Topic(p *Plan, _ rules.Action) ([]kcmigrate.Note, error) {
	policy := valueOr(p.config, "replication.policy.class", mm2DefaultPolicy)
	var tmpl string
	switch policy {
	case mm2IdentityPolicy, "IdentityReplicationPolicy":
		tmpl = collectionTemplate
	case mm2DefaultPolicy, "DefaultReplicationPolicy":
		alias, ok := p.config["source.cluster.alias"]
		if !ok {
			return nil, nil // required, no default: the require-key note blocks; no prefix is guessed
		}
		prefix := alias + valueOr(p.config, "replication.policy.separator", mm2DefaultSep)
		if strings.ContainsAny(prefix, "{}") {
			return []kcmigrate.Note{{
				Code: kcmigrate.CodeTopicNameUnsupported.Reason(), Blocking: true, Rule: FinishMM2Topic,
				Reason: "The source cluster alias or the replication separator contains '{' or '}', which cannot be written into a topic template.",
				Fix:    "Set the destination topic on the pipeline by hand.",
			}}, nil
		}
		tmpl = prefix + collectionTemplate
	default:
		return nil, nil
	}
	if _, _, err := p.Set(SideDestination, "topic", tmpl, "replication.policy"); err != nil {
		return nil, cerrors.Errorf(FinishMM2Topic+": %w", err)
	}
	return nil, nil
}

// requiredFix suggests --worker only where the worker file can hold the
// value: the brokers of Kafka Connect's own client. Nothing in a worker
// file names a sink's topics or a MirrorMaker source cluster.
func requiredFix(p *Plan, side, name, env string) string {
	fix := fmt.Sprintf("Set %s in the environment (see secrets.env.template)", env)
	if name == "servers" && side == p.connectSide() {
		return fix + ", or pass the worker file with --worker if it holds bootstrap.servers."
	}
	return fix + "."
}

const layerRequired = 100

func valueOr(cfg map[string]string, key, def string) string {
	if v, ok := cfg[key]; ok {
		return v
	}
	return def
}

// Required lists the settings a plugin requires for a role ("source" or
// "destination"), from its specification.
type Required func(plugin, role string) []string

// RequireSettings writes a variable for every setting a side's plugin
// requires that nothing set, and returns one blocking kc.setting_required
// note per setting: the pipeline would not start without it, and no Kafka
// Connect key says what it should be (MirrorMaker's records go to the
// worker's cluster, for example, so without --worker the destination's
// servers are unknown).
func RequireSettings(p *Plan, required Required) []kcmigrate.Note {
	var notes []kcmigrate.Note
	for _, side := range []string{SideSource, SideDestination} {
		c := p.connector(side)
		if c == nil {
			continue
		}
		for _, name := range required(c.Plugin, side) {
			if _, ok := c.Settings[name]; ok {
				continue
			}
			env := p.envs.claim(strings.Trim(envUnsafe.ReplaceAllString("KC_"+strings.ToUpper(p.ID+"_"+side+"_"+name), "_"), "_"), p.ID+"\x00required "+side+" "+name)
			// Above every layer, so a setting a blocked key claims (and
			// left unset) still gets its variable; it writes only unset
			// settings, so it never replaces a value.
			p.origin, p.layer = OriginConnector, layerRequired
			if _, _, err := p.Set(side, name, "${"+env+"}", "(required "+side+" "+name+")"); err != nil {
				continue // cannot happen: the setting is unset
			}
			p.secrets = append(p.secrets, Secret{Env: env, Connector: p.ID, Key: name, Origin: fmt.Sprintf("required by %s (%s)", c.Plugin, side)})
			notes = append(notes, kcmigrate.Note{
				Code: kcmigrate.CodeSettingRequired.Reason(), Blocking: true, Rule: "required/" + side + "/" + name,
				Reason: fmt.Sprintf("The %s %s requires %s, and no key of the Kafka Connect config sets it; the pipeline references ${%s} instead.", c.Plugin, side, name, env),
				Fix:    requiredFix(p, side, name, env),
			})
		}
	}
	return notes
}
