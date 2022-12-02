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
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/inspector"
	"time"

	"github.com/conduitio/conduit/pkg/record"
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
	// ParentType defines the parent type of a processor.
	ParentType int
	// ProvisionType defines provisioning type
	ProvisionType int
)

// Interface is the interface that represents a single message processor that
// can be executed on one record and manipulate it.
type Interface interface {
	// Process runs the processor function on a record.
	Process(ctx context.Context, record record.Record) (record.Record, error)

	Inspect(ctx context.Context, direction string) *inspector.Session
}

// FuncWrapper is an adapter allowing use of a function as an Interface.
type FuncWrapper struct {
	f       func(context.Context, record.Record) (record.Record, error)
	inInsp  *inspector.Inspector
	outInsp *inspector.Inspector
}

func NewFuncWrapper(f func(context.Context, record.Record) (record.Record, error)) FuncWrapper {
	// todo use real logger
	return FuncWrapper{
		f:       f,
		inInsp:  inspector.New(log.Nop(), 1000),
		outInsp: inspector.New(log.Nop(), 1000),
	}
}

func (f FuncWrapper) Process(ctx context.Context, inRec record.Record) (record.Record, error) {
	// todo same behavior as in procjs, probably can be enforced
	f.inInsp.Send(ctx, inRec)
	outRec, err := f.f(ctx, inRec)
	f.outInsp.Send(ctx, outRec)
	return outRec, err
}

func (f FuncWrapper) Inspect(ctx context.Context, direction string) *inspector.Session {
	switch direction {
	case "in":
		return f.inInsp.NewSession(ctx)
	case "out":
		return f.outInsp.NewSession(ctx)
	default:
		panic("unknown direction: " + direction)
	}
}

// Instance represents a processor instance.
type Instance struct {
	ID            string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ProvisionedBy ProvisionType

	Type      string
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
}
