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
	owner      map[string]string // config path -> key that set it
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
// rule file emits: one source and one destination. envs is the run's
// variable registry.
func NewPlan(name string, emits []rules.Emit, envs *EnvNames) (*Plan, error) {
	p := &Plan{ID: name, File: "pipelines/" + fileUnsafe.ReplaceAllString(name, "_") + ".yaml", owner: map[string]string{}, envs: envs}
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

// Kafka-facing sides. A Kafka Connect connector talks to Kafka through a
// producer (sources) or a consumer (sinks); MirrorMaker has both. Shared
// rules name these instead of source or destination.
const (
	SideKafka    = "kafka"    // every builtin:kafka connector of the pipeline
	SideProducer = "producer" // the destination, when it is builtin:kafka
	SideConsumer = "consumer" // the source, when it is builtin:kafka
)

const kafkaPlugin = "builtin:kafka"

// ErrNoSide is returned by Set when the pipeline has no connector on the
// named side, for example a producer setting on a sink's pipeline.
var ErrNoSide = cerrors.New("the pipeline has no connector on this side")

// sides resolves a side name to connector indexes.
func (p *Plan) sides(side string) ([]int, error) {
	var out []int
	for i, c := range p.connectors {
		kafka := c.Plugin == kafkaPlugin
		switch side {
		case SideSource, SideDestination:
			if c.Type == side {
				out = append(out, i)
			}
		case SideKafka:
			if kafka {
				out = append(out, i)
			}
		case SideProducer:
			if kafka && c.Type == SideDestination {
				out = append(out, i)
			}
		case SideConsumer:
			if kafka && c.Type == SideSource {
				out = append(out, i)
			}
		default:
			return nil, cerrors.Errorf("unknown side %q", side)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoSide
	}
	return out, nil
}

// Set writes setting on every connector of side for key and returns the
// JSON pointer to the first. It fails with *ConflictError if another key
// owns the setting on any of them (nothing is written then), with ErrNoSide
// when the side does not exist in this pipeline, and with an error for an
// unknown side.
func (p *Plan) Set(side, setting, value, key string) (string, error) {
	idx, err := p.sides(side)
	if err != nil {
		return "", err
	}
	paths := make([]string, len(idx))
	for j, i := range idx {
		paths[j] = fmt.Sprintf("/pipelines/0/connectors/%d/settings/%s", i, escapePointer(setting))
		if owner, ok := p.owner[paths[j]]; ok && owner != key {
			return "", &ConflictError{Setting: setting, Owner: owner}
		}
	}
	for j, i := range idx {
		c := &p.connectors[i]
		if c.Settings == nil {
			c.Settings = map[string]string{}
		}
		c.Settings[setting] = value
		p.owner[paths[j]] = key
	}
	return paths[0], nil
}

// Has reports whether setting is written on every connector of side. A
// side the pipeline lacks counts as written, so defaults skip it.
func (p *Plan) Has(side, setting string) bool {
	idx, err := p.sides(side)
	if err != nil {
		return true
	}
	for _, i := range idx {
		if _, ok := p.connectors[i].Settings[setting]; !ok {
			return false
		}
	}
	return true
}

// Secrets returns the environment variables the pipeline references.
func (p *Plan) Secrets() []Secret { return p.secrets }

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
