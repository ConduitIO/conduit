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

	"github.com/conduitio/conduit/pkg/plugin"
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
}

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
	if !isBuiltinLog(d.Plugin) {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(d.Settings["payload"]), "true")
}

// isBuiltinLog reports whether plugin names the built-in log connector at any
// version: "builtin:log", "builtin:log@v0.8.0", "builtin:log@latest", and a
// bare "log" or "log@..." (type "any", which resolves to the built-in connector
// unless a standalone plugin of that name is installed; treating it as the
// built-in fails closed). A standalone plugin named log is a different
// connector and is not matched.
func isBuiltinLog(name string) bool {
	fn := plugin.FullName(name)
	switch fn.PluginType() {
	case plugin.PluginTypeBuiltin, plugin.PluginTypeAny:
		return fn.PluginName() == "log"
	default:
		return false
	}
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
