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

//go:generate mockgen -destination=mock/connector.go -package=mock -mock_names=Source=Source,Destination=Destination . Source,Destination
//go:generate stringer -type=Type -trimprefix Type

package connector

import (
	"context"
	"time"

	"github.com/conduitio/conduit/pkg/record"
)

const (
	TypeSource Type = iota + 1
	TypeDestination
)

type (
	// Type defines the connector type.
	Type int
	// Status defines the running status of a connector.
	Status int
)

type Connector interface {
	ID() string
	Type() Type

	Config() Config
	SetConfig(Config)

	CreatedAt() time.Time
	UpdatedAt() time.Time
	SetUpdatedAt(time.Time)

	// IsRunning returns true if the connector is running and ready to accept
	// calls to Read or Write (depending on the connector type).
	IsRunning() bool
	// Validate checks if the connector is set up correctly.
	Validate(ctx context.Context, settings map[string]string) error

	// Errors returns a channel that is used to signal the node that the
	// connector experienced an error when it was processing something
	// asynchronously (e.g. persisting state).
	Errors() <-chan error

	// Open will start the plugin process and call the Open method on the
	// plugin. After the connector has been successfully opened it is considered
	// as running (IsRunning returns true) and can be stopped again with
	// Teardown. Open will return an error if called on a running connector.
	Open(context.Context) error

	// Teardown will call the Teardown method on the plugin and stop the plugin
	// process. After the connector has been successfully torn down it is
	// considered as stopped (IsRunning returns false) and can be opened again
	// with Open. Teardown will return an error if called on a stopped
	// connector.
	Teardown(context.Context) error
}

// Source is a connector that can read records from a source.
type Source interface {
	Connector

	State() SourceState
	SetState(state SourceState)

	// Read reads data from a data source and returns the record for the
	// requested position.
	Read(context.Context) (record.Record, error)

	// Ack signals to the source that the message has been successfully
	// processed and can be acknowledged.
	Ack(context.Context, record.Position) error

	// Stop signals to the source to stop producing records. Note that after
	// this call Read can still produce records that have been cached by the
	// connector.
	Stop(context.Context) error
}

// Destination is a connector that can write records to a destination.
type Destination interface {
	Connector

	State() DestinationState
	SetState(state DestinationState)

	// Write sends a record to the connector and returns nil if the record was
	// successfully received. This does not necessarily mean that the record was
	// successfully processed and written to the 3rd party system, it might have
	// been cached and will be written at a later point in time. Acknowledgments
	// can be received through Ack to figure out if a record was actually
	// processed or if an error happened while processing it.
	Write(context.Context, record.Record) error
	// Ack blocks until an acknowledgment is received that a record was
	// processed and returns the position of that record. If the record wasn't
	// successfully processed the function returns the position and an error.
	Ack(context.Context) (record.Position, error)
}

// Config collects common data stored for a connector.
type Config struct {
	Name       string
	Settings   map[string]string
	Plugin     string
	PipelineID string

	ProcessorIDs []string
}

type SourceState struct {
	Position record.Position
}

type DestinationState struct {
	Positions map[string]record.Position
}
