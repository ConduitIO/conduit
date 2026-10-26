// Copyright © 2022 Meroxa, Inc.
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

//go:generate stringer -type=Status -trimprefix Status

package pipeline

import (
	"strings"
	"sync"
	"time"
)

const (
	StatusRunning Status = iota + 1
	StatusSystemStopped
	StatusUserStopped
	StatusDegraded
	StatusRecovering
)

const (
	ProvisionTypeAPI ProvisionType = iota
	ProvisionTypeConfig
)

type (
	// Status defines the running status of a pipeline.
	Status int
	// ProvisionType defines provisioning type
	ProvisionType int
)

// Instance manages a collection of Connectors, which
// can be either Destination or Source. The pipeline sets up its publishers and
// subscribers based on whether the Connector in question is a Destination or a Source.
type Instance struct {
	ID            string
	Config        Config
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ProvisionedBy ProvisionType
	DLQ           DLQ

	ConnectorIDs []string
	ProcessorIDs []string

	status     Status
	statusLock sync.RWMutex
}

// encodableInstance is an encodable "view" of Instance
// through which we can also encode an Instance's unexported fields.
type encodableInstance struct {
	*Instance
	Status Status
}

// Config holds configuration data for building a pipeline.
type Config struct {
	Name        string
	Description string
}

type DLQ struct {
	Plugin   string
	Settings map[string]string

	WindowSize          int
	WindowNackThreshold int
	// SettingRefs is written by Conduit v0.21 and later, which stores ${VAR}
	// templates here and removes the matching keys from Settings. This release
	// does not understand them: the field is decoded only so that it survives a
	// re-write of the record (Invariant 6: nothing is silently dropped) and so
	// the lifecycle can refuse to run the pipeline instead of starting it with
	// those settings absent (Invariant 3: no silent change of what a pipeline
	// delivers to). The JSON key name and the map[string]string type are a
	// compatibility contract with v0.21 (design 20261014-secrets-at-rest.md);
	// the key is matched case-insensitively by the decoder. Never resolve or
	// log the values.
	SettingRefs map[string]string `json:",omitempty"`
}

// HasSettingRefs reports whether the DLQ carries SettingRefs written by a
// newer Conduit.
func (d DLQ) HasSettingRefs() bool { return len(d.SettingRefs) > 0 }

// builtinLogPlugin is the plugin name of the built-in log connector, the
// default DLQ.
const builtinLogPlugin = "builtin:log"

var DefaultDLQ = DLQ{
	Plugin: builtinLogPlugin,
	Settings: map[string]string{
		"level":   "warn",
		"message": "record delivery failed",
	},
	WindowSize:          1,
	WindowNackThreshold: 0,
}

// LogsWithoutPayload reports whether the DLQ writes a nacked record to the
// built-in log connector without payload:true. Such a DLQ preserves only
// hashes of the key and position, so a record routed to it is unrecoverable.
// It does not say whether any record is routed to it; see RoutesNacks and
// DropsRecordContent.
func (d DLQ) LogsWithoutPayload() bool {
	if d.Plugin != builtinLogPlugin {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(d.Settings["payload"]), "true")
}

// RoutesNacks reports whether a nacked record is written to the DLQ at all.
// Both engines' DLQ windows (pkg/lifecycle/stream/dlq.go and
// pkg/lifecycle-poc/funnel/dlq.go) route a nack when the window is disabled
// (WindowSize 0) or the nack threshold is above 0. With a window and a
// threshold of 0, which is the default, the first nack stops the pipeline with
// the nack's error: nothing is written to the DLQ and the record is not acked,
// so it is redelivered after a restart (invariant 3 holds without the DLQ).
// TestRoutesNacks_MatchesWindow in both packages pins this to the windows.
func (d DLQ) RoutesNacks() bool {
	return d.WindowSize <= 0 || d.WindowNackThreshold > 0
}

// DropsRecordContent reports whether a nacked record would be acked upstream
// while only hashes of it are kept: the DLQ receives nacked records
// (RoutesNacks) and logs them without payload (LogsWithoutPayload). A start is
// refused for such a DLQ (invariant 3, GHSA-m3mw-fpw8-w765). The default DLQ
// does not drop record content, because it routes no nacks.
func (d DLQ) DropsRecordContent() bool {
	return d.RoutesNacks() && d.LogsWithoutPayload()
}

func (p *Instance) SetStatus(s Status) {
	p.statusLock.Lock()
	defer p.statusLock.Unlock()

	p.status = s
}

func (p *Instance) GetStatus() Status {
	p.statusLock.RLock()
	defer p.statusLock.RUnlock()

	return p.status
}
