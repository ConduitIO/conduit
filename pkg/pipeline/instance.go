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
}

var DefaultDLQ = DLQ{
	Plugin: "builtin:log",
	Settings: map[string]string{
		"level":   "warn",
		"message": "record delivery failed",
	},
	WindowSize:          1,
	WindowNackThreshold: 0,
}

func (p *Instance) Connectors() []string {
	v := make([]string, len(p.ConnectorIDs))
	_ = copy(v, p.ConnectorIDs)

	return v
}

func (p *Instance) Processors() []string {
	v := make([]string, len(p.ProcessorIDs))
	_ = copy(v, p.ProcessorIDs)

	return v
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
