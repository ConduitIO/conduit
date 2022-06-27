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

//go:generate mockgen -destination=mock/processor.go -package=mock -mock_names=Interface=Interface . Interface
//go:generate stringer -type=ParentType -trimprefix ParentType

package processor

import (
	"context"
	"time"

	"github.com/conduitio/conduit/pkg/record"
)

const (
	ParentTypeConnector ParentType = iota + 1
	ParentTypePipeline
)

// ParentType defines the parent type of a processor.
type ParentType int

// Interface is the interface that represents a single message processor that
// can be executed on one record and manipulate it.
type Interface interface {
	// Process runs the processor function on a record.
	Process(ctx context.Context, record record.Record) (record.Record, error)
}

// InterfaceFunc is a stateless processor.Interface implementation
// which is using a Go function to process records.
type InterfaceFunc func(context.Context, record.Record) (record.Record, error)

func (p InterfaceFunc) Process(ctx context.Context, record record.Record) (record.Record, error) {
	return p(ctx, record)
}

// Instance represents a processor instance.
type Instance struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
	// Name is the name of the processor under which it is registered in the
	// builder registry.
	Name      string
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
