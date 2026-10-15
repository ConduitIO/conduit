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
)

var finishers = map[string]Finisher{
	FinishMM2Topic: mm2Topic,
	FinishDefault:  defaultSetting,
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
	mm2DefaultAlias   = "source"
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
		prefix := valueOr(p.config, "source.cluster.alias", mm2DefaultAlias) + valueOr(p.config, "replication.policy.separator", mm2DefaultSep)
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
