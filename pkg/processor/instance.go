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

//go:generate mockgen -destination=mock/processor.go -package=mock -mock_names=Interface=Processor . Interface
//go:generate stringer -type=ParentType -trimprefix ParentType

package processor

import (
	"context"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"time"

	"github.com/conduitio/conduit/pkg/inspector"
)

const (
	ParentTypeConnector ParentType = iota + 1
	ParentTypePipeline
)

const (
	ProvisionTypeAPI ProvisionType = iota
	ProvisionTypeConfig
)

type (
	// ParentType defines the parent type of processor.
	ParentType int
	// ProvisionType defines provisioning type
	ProvisionType int
)

// Interface is the interface that represents a single message processor that
// can be executed on one record and manipulate it.
type Interface interface {
	sdk.Processor

	// InspectIn starts an inspection session for input records for this processor.
	InspectIn(ctx context.Context, id string) *inspector.Session
	// InspectOut starts an inspection session for output records for this processor.
	InspectOut(ctx context.Context, id string) *inspector.Session
	// Close closes this processor and releases any resources
	// which may have been used by it.
	Close()
}

// Instance represents a processor instance.
// todo move inspectin and inspectout into instance
type Instance struct {
	ID            string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ProvisionedBy ProvisionType

	// todo rename to plugin
	Type string
	// Condition is a goTemplate formatted string, the value provided to the template is a sdk.Record, it should evaluate
	// to a boolean value, indicating a condition to run the processor for a specific record or not. (template functions
	// provided by `sprig` are injected)
	Condition string
	Parent    Parent
	Config    Config
	Processor Interface
}

// Parent represents the connection to the entity a processor is connected to.
type Parent struct {
	// ID of the parent.
	ID string
	// Type of the parent.
	Type ParentType
}

// Config holds configuration data for building a processor.
type Config struct {
	Settings map[string]string
	Workers  int
}
