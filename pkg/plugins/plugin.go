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

package plugins

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/hashicorp/go-plugin"
)

var (
	Handshake = plugin.HandshakeConfig{
		ProtocolVersion:  1,
		MagicCookieKey:   "CONDUIT_PLUGIN",
		MagicCookieValue: "CONDUIT",
	}
	PluginMap = NewPluginMap(nil, nil, nil)

	// ErrEndData is the return value when a Source has no new records.
	ErrEndData = NewRecoverableError(cerrors.New("ErrEndData"))
)

// Run can be called in the main function of the plugin to start the plugin
// server with the correct configuration.
func Run(source Source, destination Destination, spec Specifier) {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         NewPluginMap(source, destination, spec),
		GRPCServer:      plugin.DefaultGRPCServer,
	})
}

// NewPluginMap creates a new plugin map with the supplied source and destination. The
// return value can be used for field plugin.ServeConfig.Plugins when starting
// the plugin manually, although it is encouraged to use Run instead.
func NewPluginMap(source Source, destination Destination, spec Specifier) map[string]plugin.Plugin {
	return map[string]plugin.Plugin{
		"source":      &SourcePlugin{Impl: source},
		"destination": &DestinationPlugin{Impl: destination},
		"specifier":   &SpecificationPlugin{Impl: spec},
	}
}

// Config contains the configuration for the Connector.
type Config struct {
	Settings map[string]string
}

// Specification is returned by a plugin when Specify is called.
// It contains information about the configuration parameters for plugins
// and allows them to describe their parameters.
type Specification struct {
	// a brief description of the plugins in this package and what they do
	Summary string
	// Description is a more long form area appropriate for README-like text
	// that the author can provide for documentation about the specified
	// Parameters.
	Description string
	// Version string. Should be prepended with `v` like Go, e.g. `v1.54.3`
	Version string
	// Author declares the entity that created or maintains this plugin.
	Author string
	// DestinationParams and SourceParams is a map of named Parameters that describe
	// how to configure a the plugin's Destination or Source.
	DestinationParams map[string]Parameter
	SourceParams      map[string]Parameter
}

// Parameter is a helper struct for defining plugin Specifications.
type Parameter struct {
	// Default is the default value of the parameter, if any.
	Default string
	// Required is whether it must be provided in the Config or not.
	Required bool
	// Description holds a description of the field and how to configure it.
	Description string
}

// Specifier allows a plugin to return its Specification to any caller.
type Specifier interface {
	Specify() (Specification, error)
}

//  Connector defines our Connector interface.
type Connector interface {
	Open(ctx context.Context, cfg Config) error
	Teardown() error
	Validate(cfg Config) error
}

// Source reads from a source and pipes it into our system.
type Source interface {
	Connector

	// Read reads data from a data source and returns the record for the
	// requested position.
	// Note, the Read method returns not the record at the position passed to
	// it, but the next one. If the position is nil, first record is returned.
	// When the position of the first record is passed, a second record is
	// returned etc.
	Read(context.Context, record.Position) (record.Record, error)

	// Ack signals to the source that the message has been successfully
	// processed and can be acknowledged. Sources that don't need to ack the
	// message should return nil.
	Ack(context.Context, record.Position) error
}

// Destination gets notified anytime anything comes out of the stream
type Destination interface {
	Connector

	// Write is writing records to a Destination.
	// The returned position is the position of the record written.
	// Important note: A Destination stops on first error returned from Write.
	Write(context.Context, record.Record) (record.Position, error)
}
