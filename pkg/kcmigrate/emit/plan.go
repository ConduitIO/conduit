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

// Package emit turns classified Kafka Connect keys into a Conduit pipeline:
// version 2.2 YAML that `conduit pipelines validate` accepts. Rule data names
// an emit kind per key; the kinds are the code in this package.
//
// Invariants:
//   - A setting is written by at most one key. A second key that maps onto
//     the same setting turns into a blocking kc.key_conflict entry instead
//     of silently overwriting the first.
//   - A secret value never reaches a pipeline file: it is replaced by a
//     ${VAR} reference and listed, without its value, in
//     secrets.env.template.
//   - Output is deterministic: settings are written in sorted order.
package emit

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/conduitio/yaml/v3"
)

// Sides of a pipeline.
const (
	SideSource      = "source"
	SideDestination = "destination"
)

// Plan is one generated pipeline under construction.
type Plan struct {
	// ID is the pipeline id, the connector name.
	ID string
	// File is the pipeline file path relative to the output directory.
	File       string
	connectors []connectorDef
	dlq        *dlqDef
	scope      string            // rules.ScopeSource or rules.ScopeSink: which client Kafka Connect runs
	owner      map[string]owner  // config path -> origin, key and layer that set it
	superseded map[keyRef]string // origin and key -> the higher-layer key that replaced its value
	origin     string            // origin of the action being applied
	layer      int               // layer of the action being applied
	config     map[string]string // the connector's values, for finishers
	secrets    []Secret
	envs       *EnvNames
}

// Secret is an environment variable a pipeline references instead of a
// secret value. It never holds the value.
type Secret struct {
	Env       string
	Connector string
	Key       string
	Origin    string
	// Reference is the config-provider reference the value was, if any, so
	// the operator knows where the value lives.
	Reference string
	// Literal means the input held the secret itself, not a reference.
	Literal bool
}

type pipelineFile struct {
	Version   string        `yaml:"version"`
	Pipelines []pipelineDef `yaml:"pipelines"`
}

type pipelineDef struct {
	ID          string         `yaml:"id"`
	Status      string         `yaml:"status"`
	Description string         `yaml:"description,omitempty"`
	Connectors  []connectorDef `yaml:"connectors"`
	DLQ         *dlqDef        `yaml:"dead-letter-queue,omitempty"`
}

type dlqDef struct {
	WindowSize          int `yaml:"window-size"`
	WindowNackThreshold int `yaml:"window-nack-threshold"`
}

type connectorDef struct {
	ID       string            `yaml:"id"`
	Type     string            `yaml:"type"`
	Plugin   string            `yaml:"plugin"`
	Settings map[string]string `yaml:"settings,omitempty"`
}

var fileUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// EnvNames hands out the secret variables of one migration run. It is
// shared by every plan of the run, so two connectors whose names sanitize
// alike (my-conn and my_conn) never share a variable and run with each
// other's secret.
type EnvNames struct {
	owner map[string]string // variable -> connector and key it stands for
}

// NewEnvNames returns an empty registry.
func NewEnvNames() *EnvNames { return &EnvNames{owner: map[string]string{}} }

// envReferenceOwner owns the names that ${env:NAME} references use.
const envReferenceOwner = "\x00env"

// Reserve marks name as used by a ${env:NAME} reference in the run, so no
// generated variable takes it: the operator fills that variable for the
// reference, with another value.
func (n *EnvNames) Reserve(name string) { n.owner[name] = envReferenceOwner }

// ReserveReferences reserves the variable of every value that is exactly a
// ${env:NAME} reference.
func (n *EnvNames) ReserveReferences(values []string) {
	for _, v := range values {
		if m := envReference.FindStringSubmatch(strings.TrimSpace(v)); m != nil {
			n.Reserve(m[1])
		}
	}
}

// claim returns name, or name_2, name_3, ... if another owner has it.
func (n *EnvNames) claim(name, owner string) string {
	candidate := name
	for i := 2; ; i++ {
		if o, ok := n.owner[candidate]; !ok || o == owner {
			n.owner[candidate] = owner
			return candidate
		}
		candidate = fmt.Sprintf("%s_%d", name, i)
	}
}

// NewPlan starts a pipeline for connector name with the plugins the class's
// rule file emits: one source and one destination. scope is the class's
// Kafka Connect connector type, rules.ScopeSource or rules.ScopeSink; it
// decides which pipeline connector Kafka Connect's own Kafka client becomes.
// envs is the run's variable registry.
func NewPlan(name, scope string, emits []rules.Emit, envs *EnvNames) (*Plan, error) {
	if scope != rules.ScopeSource && scope != rules.ScopeSink {
		return nil, cerrors.Errorf("scope %q is not a connector type", scope)
	}
	p := &Plan{
		ID: name, File: "pipelines/" + fileUnsafe.ReplaceAllString(name, "_") + ".yaml", scope: scope,
		owner: map[string]owner{}, superseded: map[keyRef]string{}, origin: OriginConnector, envs: envs,
	}
	for _, side := range []string{SideSource, SideDestination} {
		var plugin string
		for _, e := range emits {
			if e.Role == side {
				if plugin != "" {
					return nil, cerrors.Errorf("rule file emits two %s plugins", side)
				}
				plugin = e.Plugin
			}
		}
		if plugin == "" {
			return nil, cerrors.Errorf("rule file emits no %s plugin", side)
		}
		p.connectors = append(p.connectors, connectorDef{ID: side, Type: side, Plugin: plugin})
	}
	return p, nil
}

// Plugin returns the plugin of side.
func (p *Plan) Plugin(side string) string {
	if c := p.connector(side); c != nil {
		return c.Plugin
	}
	return ""
}

func (p *Plan) connector(side string) *connectorDef {
	for i := range p.connectors {
		if p.connectors[i].Type == side {
			return &p.connectors[i]
		}
	}
	return nil
}

// ConflictError is returned by Set when a setting already has another owner.
type ConflictError struct {
	Setting, Owner string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("setting %s is already set by %s", e.Setting, e.Owner)
}

// Kafka-facing sides: Kafka Connect's own client of a connector, a producer
// for a source connector and a consumer for a sink. In the pipeline it is
// the builtin:kafka destination of a source, or the builtin:kafka source of
// a sink. Shared and worker rules name these instead of source or
// destination. A pipeline may have a second Kafka connector (MirrorMaker's
// source is its own consumer of another cluster); no shared or worker key
// reaches that one, as no worker key reaches MirrorMaker's consumer.
const (
	SideKafka    = "kafka"    // Kafka Connect's client, producer or consumer
	SideProducer = "producer" // Kafka Connect's client, if it is a producer
	SideConsumer = "consumer" // Kafka Connect's client, if it is a consumer
)

const kafkaPlugin = "builtin:kafka"

// ErrNoSide is returned by Set when the pipeline has no connector on the
// named side, for example a producer setting on a sink's pipeline.
var ErrNoSide = cerrors.New("the pipeline has no connector on this side")

// connectSide is the pipeline side Kafka Connect's own client becomes: the
// destination of a source connector, the source of a sink.
func (p *Plan) connectSide() string {
	if p.scope == rules.ScopeSink {
		return SideSource
	}
	return SideDestination
}

// sides resolves a side name to connector indexes.
func (p *Plan) sides(side string) ([]int, error) {
	var want string
	switch side {
	case SideSource, SideDestination:
		want = side
	case SideKafka:
		want = p.connectSide()
	case SideProducer:
		if p.scope == rules.ScopeSource {
			want = SideDestination
		}
	case SideConsumer:
		if p.scope == rules.ScopeSink {
			want = SideSource
		}
	default:
		return nil, cerrors.Errorf("unknown side %q", side)
	}
	kafkaOnly := side != SideSource && side != SideDestination
	var out []int
	for i, c := range p.connectors {
		if want != "" && c.Type == want && (!kafkaOnly || c.Plugin == kafkaPlugin) {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoSide
	}
	return out, nil
}

// Origins of a key: the worker file or the connector config. A worker key
// and a connector key of the same name (producer.sasl.mechanism, say) are
// different keys.
const (
	OriginWorker    = "worker"
	OriginConnector = "connector"
)

// keyRef names a key with its origin.
type keyRef struct{ origin, key string }

// owner records which key wrote a setting, and at which layer.
type owner struct {
	keyRef
	layer int
}

// Layers order the sources Kafka Connect merges into one client or
// connector config, lowest first (Worker.baseProducerConfigs and
// baseConsumerConfigs, Apache Kafka 4.2.0, Worker.java:837-874 and 911-942):
// worker keys, then the worker's producer./consumer.-prefixed keys, then the
// connector's own keys, then its producer.override./consumer.override. keys.
// A rule's emit action picks its place within its file's base with a named
// layer (see layerOffsets); LayerFallback is below every other layer and is
// used as a whole or not at all (ApplyAll).
const (
	LayerFallback  = 5
	LayerWorker    = 10
	LayerConnector = 30
)

// Set writes setting on every connector of side for key, at the layer of the
// action being applied, and returns the JSON pointer to the first. A higher
// layer replaces a lower one, as in Kafka Connect, and the replaced key is
// recorded (Superseded). If a higher layer already set it, nothing is
// written and overriddenBy names that key. Two keys at the same layer fail
// with *ConflictError. ErrNoSide when the side does not exist in this
// pipeline; an error for an unknown side.
func (p *Plan) Set(side, setting, value, key string) (path, overriddenBy string, err error) {
	idx, err := p.sides(side)
	if err != nil {
		return "", "", err
	}
	// Each connector of the side is decided on its own: a worker
	// bootstrap.servers for every Kafka connector may lose on the source
	// (a connector key set it) and win on the destination.
	var write []int
	var paths []string
	for _, i := range idx {
		path := fmt.Sprintf("/pipelines/0/connectors/%d/settings/%s", i, escapePointer(setting))
		o, ok := p.owner[path]
		me := keyRef{origin: p.origin, key: key}
		switch {
		// The same key may write a setting again at its own layer (a kind
		// that writes one setting on two connectors); a key of the same
		// name from another origin is another key, decided by layer.
		case !ok || o.layer < p.layer || (o.layer == p.layer && o.keyRef == me):
			write = append(write, i)
			paths = append(paths, path)
		case o.layer > p.layer:
			overriddenBy = o.key
		default:
			return "", "", &ConflictError{Setting: setting, Owner: o.key}
		}
	}
	if len(write) == 0 {
		return fmt.Sprintf("/pipelines/0/connectors/%d/settings/%s", idx[0], escapePointer(setting)), overriddenBy, nil
	}
	me := keyRef{origin: p.origin, key: key}
	for j, i := range write {
		if o, ok := p.owner[paths[j]]; ok && o.keyRef != me {
			p.superseded[o.keyRef] = key
		}
		c := &p.connectors[i]
		if c.Settings == nil {
			c.Settings = map[string]string{}
		}
		c.Settings[setting] = value
		p.owner[paths[j]] = owner{keyRef: me, layer: p.layer}
	}
	return paths[0], "", nil
}

// supersededBy returns the key that replaced the value key of origin wrote,
// if a higher layer replaced it.
func (p *Plan) supersededBy(origin, key string) (string, bool) {
	by, ok := p.superseded[keyRef{origin: origin, key: key}]
	return by, ok
}

// Secrets returns the environment variables the pipeline references. A
// variable whose setting a higher layer replaced is left out: the operator
// would be asked to fill a variable nothing reads.
func (p *Plan) Secrets() []Secret {
	var out []Secret
	for _, s := range p.secrets {
		if p.references("${" + s.Env + "}") {
			out = append(out, s)
		}
	}
	return out
}

func (p *Plan) references(ref string) bool {
	for _, c := range p.connectors {
		for _, v := range c.Settings {
			if strings.Contains(v, ref) {
				return true
			}
		}
	}
	return false
}

// YAML renders the pipeline file.
func (p *Plan) YAML(status, description string) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(pipelineFile{
		Version: "2.2",
		Pipelines: []pipelineDef{{
			ID: p.ID, Status: status, Description: description, Connectors: p.connectors, DLQ: p.dlq,
		}},
	})
	if err == nil {
		err = enc.Close()
	}
	if err != nil {
		return nil, cerrors.Errorf("encode pipeline %s: %w", p.ID, err)
	}
	return buf.Bytes(), nil
}

func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// HaltOnFailure writes a dead-letter queue that stops the pipeline on the
// first failed record (window 1, threshold 0), Kafka Connect's
// errors.tolerance=none. It is Conduit's default too; writing it explicitly
// keeps the behavior if the default ever changes.
func (p *Plan) HaltOnFailure() string {
	p.dlq = &dlqDef{WindowSize: 1, WindowNackThreshold: 0}
	return "/pipelines/0/dead-letter-queue"
}
