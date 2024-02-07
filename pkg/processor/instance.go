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

//go:generate stringer -type=ParentType -trimprefix ParentType
//go:generate mockgen -destination=mock/processor_registry.go -package=mock -mock_names=Registry=Registry . Registry

package processor

import (
	"context"
	"time"

	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
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

type Registry interface {
	Get(ctx context.Context, pluginName string, id string) (sdk.Processor, error)
}

// Instance represents a processor instance.
type Instance struct {
	ID            string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ProvisionedBy ProvisionType

	Plugin string
	// Condition is a goTemplate formatted string, the value provided to the template is a sdk.Record, it should evaluate
	// to a boolean value, indicating a condition to run the processor for a specific record or not. (template functions
	// provided by `sprig` are injected)
	Condition string
	Parent    Parent
	Config    Config

	inInsp  *inspector.Inspector
	outInsp *inspector.Inspector
	running bool
}

func (i *Instance) init(logger log.CtxLogger) {
	i.inInsp = inspector.New(logger, inspector.DefaultBufferSize)
	i.outInsp = inspector.New(logger, inspector.DefaultBufferSize)
}

func (i *Instance) InspectIn(ctx context.Context, id string) *inspector.Session {
	return i.inInsp.NewSession(ctx, id)
}

func (i *Instance) InspectOut(ctx context.Context, id string) *inspector.Session {
	return i.outInsp.NewSession(ctx, id)
}

func (i *Instance) Close() {
	i.inInsp.Close()
	i.outInsp.Close()
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
