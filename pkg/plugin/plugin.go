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

//go:generate mockgen -destination=mock/plugin.go -package=mock -mock_names=Dispenser=Dispenser,SourcePlugin=SourcePlugin,DestinationPlugin=DestinationPlugin,SpecifierPlugin=SpecifierPlugin . Dispenser,DestinationPlugin,SourcePlugin,SpecifierPlugin

package plugin

import (
	"context"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/conduitio/conduit/pkg/record"
)

// Dispenser dispenses specifier, source and destination plugins.
type Dispenser interface {
	DispenseSpecifier() (SpecifierPlugin, error)
	DispenseSource() (SourcePlugin, error)
	DispenseDestination() (DestinationPlugin, error)
}

type SourcePlugin interface {
	// Configure provides the configuration to the plugin and sets it up, so it's
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
	// records that might be cached. The response will contain the position of
	// the last record in the stream. Conduit should keep reading records until
	// it encounters the record with the last position. After it received all
	// records and sent back acks for all successfully processed records it
	// should call Teardown to close the stream. After the stream is closed the
	// Read method will return the appropriate error signaling the stream is
	// closed.
	Stop(context.Context) (record.Position, error)

	// Teardown is the last call that must be issued before discarding the
	// plugin. It signals to the plugin it can release any open resources and
	// prepare for a graceful shutdown.
	Teardown(context.Context) error

	// LifecycleOnCreated should be called after Configure and before Start when
	// the connector is run for the first time. This call should be skipped if
	// the connector was already started before.
	LifecycleOnCreated(ctx context.Context, cfg map[string]string) error
	// LifecycleOnUpdated should be called after Configure and before Start when
	// the connector configuration has changed since the last run. This call
	// should be skipped if the connector configuration did not change.
	LifecycleOnUpdated(ctx context.Context, cfgBefore, cfgAfter map[string]string) error
	// LifecycleOnDeleted should be called when the connector is deleted. It
	// should be the only method that is called in that case.
	LifecycleOnDeleted(ctx context.Context, cfg map[string]string) error
}

type DestinationPlugin interface {
	// Configure provides the configuration to the plugin and sets it up, so it's
	// ready to start running. If the configuration is invalid the method will
	// return an error.
	Configure(context.Context, map[string]string) error

	// Start will trigger a background process in the plugin that will stream
	// records to the plugin and listen to acks. After Start returns Conduit is
	// allowed to call methods Write and Ack. The stream will keep running until
	// the context passed to Start is closed. If the context is closed no more
	// records or acks can be passed between Conduit or the plugin (hard stop).
	// To stop the stream gracefully use the method Stop.
	Start(context.Context) error

	// Write sends a record to the plugin and returns nil if the record was
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

	// Stop signals to the plugin that the record with the specified position is
	// the last one and no more records will be written to the stream after it.
	// Once the plugin receives the last record it should flush any records that
	// might be cached and not yet written to the 3rd party resource.

	// Stop should be called to invoke a graceful shutdown of the stream. It
	// will signal the plugin that after receiving the record with the last
	// position no more records will be written to the stream and that the
	// plugin should flush any records that might be cached. The stream will
	// still remain open so Conduit can fetch the remaining acks. After all acks
	// are received Conduit should call Teardown to close the stream. After the
	// stream is closed the Ack method will return the appropriate error
	// signaling the stream is closed.
	Stop(context.Context, record.Position) error

	// Teardown is the last call that must be issued before discarding the
	// plugin. It signals to the plugin it can release any open resources and
	// prepare for a graceful shutdown.
	Teardown(context.Context) error

	// LifecycleOnCreated should be called after Configure and before Start when
	// the connector is run for the first time. This call should be skipped if
	// the connector was already started before.
	LifecycleOnCreated(ctx context.Context, cfg map[string]string) error
	// LifecycleOnUpdated should be called after Configure and before Start when
	// the connector configuration has changed since the last run. This call
	// should be skipped if the connector configuration did not change.
	LifecycleOnUpdated(ctx context.Context, cfgBefore, cfgAfter map[string]string) error
	// LifecycleOnDeleted should be called when the connector was deleted. It
	// should be the only method that is called in that case.
	LifecycleOnDeleted(ctx context.Context, cfg map[string]string) error
}

type SpecifierPlugin interface {
	// Specify returns the plugin specification.
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
	// Type defines the parameter data type.
	Type ParameterType
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

type ParameterType int

const (
	ParameterTypeString ParameterType = iota + 1
	ParameterTypeInt
	ParameterTypeFloat
	ParameterTypeBool
	ParameterTypeFile
	ParameterTypeDuration
)

const (
	PluginTypeBuiltin    = "builtin"
	PluginTypeStandalone = "standalone"
	PluginTypeAny        = "any"

	PluginVersionLatest = "latest"
)

type FullName string

func NewFullName(pluginType, pluginName, pluginVersion string) FullName {
	if pluginType != "" {
		pluginType += ":"
	}
	if pluginVersion != "" {
		pluginVersion = "@" + pluginVersion
	}
	return FullName(pluginType + pluginName + pluginVersion)
}

func (fn FullName) PluginType() string {
	tokens := strings.SplitN(string(fn), ":", 2)
	if len(tokens) > 1 {
		return tokens[0]
	}
	return PluginTypeAny // default
}

func (fn FullName) PluginName() string {
	name := string(fn)

	tokens := strings.SplitN(name, ":", 2)
	if len(tokens) > 1 {
		name = tokens[1]
	}

	tokens = strings.SplitN(name, "@", 2)
	if len(tokens) > 1 {
		name = tokens[0]
	}

	return name
}

func (fn FullName) PluginVersion() string {
	tokens := strings.SplitN(string(fn), "@", 2)
	if len(tokens) > 1 {
		return tokens[len(tokens)-1]
	}
	return PluginVersionLatest // default
}

func (fn FullName) PluginVersionGreaterThan(other FullName) bool {
	leftVersion := fn.PluginVersion()
	rightVersion := other.PluginVersion()

	leftSemver, err := semver.NewVersion(leftVersion)
	if err != nil {
		return false // left is an invalid semver, right is greater either way
	}
	rightSemver, err := semver.NewVersion(rightVersion)
	if err != nil {
		return true // left is a valid semver, right is not, left is greater
	}

	return leftSemver.GreaterThan(rightSemver)
}
