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

package plugin

import (
	"context"

	"github.com/conduitio/conduit/pkg/record"
)

// Dispenser dispenses specifier, source and destination plugins.
type Dispenser interface {
	DispenseSpecifier() (SpecifierPlugin, error)
	DispenseSource() (SourcePlugin, error)
	DispenseDestination() (DestinationPlugin, error)
}

type SourcePlugin interface {
	// Configure provides the configuration to the plugin and sets it up so it's
	// ready to start running. If the configuration is invalid the method will
	// return an error.
	Configure(context.Context, map[string]string) error

	// Start will trigger a background process in the plugin that will stream
	// records to Conduit and listen to acks. After Start returns Conduit is
	// allowed to call methods Read and Ack. The stream will keep running until
	// the context passed to Start is closed. If the context is closed no more
	// records or acks can be passed between Conduit or the plugin (hard stop).
	// To stop the stream gracefully use the method Stop.
	Start(context.Context, record.Position) error

	// Read will block until the plugin returns a new record or until the stream
	// is closed (i.e. Stop is called and the plugin closes the stream). All
	// records returned by Read need to be acked using the function Ack and the
	// position of the record. Read will return ErrStreamNotOpen is the stream
	// is not open.
	Read(context.Context) (record.Record, error)
	// Ack signals to the plugin that the record with that position was
	// processed and all resources related to that record can be released.
	Ack(context.Context, record.Position) error

	// Stop should be called to invoke a graceful shutdown of the stream. It
	// will signal the plugin to stop retrieving new records and flush any
	// records that might be cached. The stream will still remain open so
	// Conduit can fetch the remaining records and send back any outstanding
	// acks. After the stream is closed the Read method will return the
	// appropriate error signaling the stream is closed.
	Stop(context.Context) error

	// Teardown is the last call that must be issued before discarding the
	// plugin. It signals to the plugin it can release any open resources and
	// prepare for a graceful shutdown.
	Teardown(context.Context) error
}

type DestinationPlugin interface {
	Configure(context.Context, map[string]string) error

	Start(context.Context) error

	Write(context.Context, record.Record) error
	Ack(context.Context) (record.Position, error)

	Stop(context.Context) error
	Teardown(context.Context) error
}

type SpecifierPlugin interface {
	Specify() (Specification, error)
}

// Specification is returned by a plugin when Specify is called.
// It contains information about the configuration parameters for plugins
// and allows them to describe their parameters.
type Specification struct {
	// Name is the name of the plugin.
	Name string
	// Summary is a brief description of the plugin and what it does.
	Summary string
	// Description is a more long form area appropriate for README-like text
	// that the author can provide for documentation about the specified
	// Parameters.
	Description string
	// Version string. Should be a semver prepended with `v`, e.g. `v1.54.3`.
	Version string
	// Author declares the entity that created or maintains this plugin.
	Author string
	// SourceParams and DestinationParams are maps of named Parameters that
	// describe how to configure the plugins Destination or Source.
	SourceParams      map[string]Parameter
	DestinationParams map[string]Parameter
}

// Parameter is a helper struct for defining plugin Specifications.
type Parameter struct {
	// Default is the default value of the parameter, if any.
	Default string
	// Required is whether it must be provided in the Config or not.
	Type string
	// Description holds a description of the field and how to configure it.
	Description string
	// Validations list of validations to check for the parameter.
	Validations []Validation
}

type Validation struct {
	Type  ValidationType
	Value string
}

type ValidationType int64

const (
	ValidationTypeRequired ValidationType = iota + 1
	ValidationTypeGreaterThan
	ValidationTypeLessThan
	ValidationTypeInclusion
	ValidationTypeExclusion
	ValidationTypeRegex
)
