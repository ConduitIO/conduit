// Copyright © 2023 Meroxa, Inc.
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

//go:generate mockgen -destination=mock/plugin.go -package=mock -mock_names=ProcessorPlugin=ProcessorPlugin . ProcessorPlugin

package processor

import (
	"context"

	"github.com/conduitio/conduit-commons/opencdc"
)

// ProcessorPlugin receives records, manipulates them and returns back the processed
// records.
//
//nolint:revive // This interface will be moved into the SDK
type ProcessorPlugin interface {
	// Specification contains the metadata of this processor like name, version,
	// description and a list of parameters expected in the configuration.
	Specification() Specification

	// Configure is the first function to be called in a processor. It provides the
	// processor with the configuration that needs to be validated and stored.
	// In case the configuration is not valid it should return an error.
	// Configure should not open connections or any other resources. It should solely
	// focus on parsing and validating the configuration itself.
	Configure(context.Context, map[string]string) error

	// Open is called after Configure to signal the processor it can prepare to
	// start writing records. If needed, the processor should open connections and
	// start background jobs in this function.
	Open(context.Context) error

	// Process takes a number of records and processes them right away.
	// It should return a slice of ProcessedRecord that matches the length of
	// the input slice. If an error occurred while processing a specific record
	// it should be reflected in the ProcessedRecord with the same index as the
	// input record that caused the error.
	// Process should be idempotent, as it may be called multiple times with the
	// same records (e.g. after a restart when records were not flushed).
	Process(context.Context, []opencdc.Record) []ProcessedRecord

	// Teardown signals to the processor that the pipeline is shutting down and
	// there will be no more calls to any other function. After Teardown returns,
	// the processor will be discarded.
	Teardown(context.Context) error
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

// ProcessedRecord is a record returned by the processor.
type ProcessedRecord interface {
	isProcessedRecord() // Ensure structs outside of this package can't implement this interface.
}

// SingleRecord is a single processed record that will continue down the pipeline.
type SingleRecord opencdc.Record

func (SingleRecord) isProcessedRecord() {}

// FilterRecord is a record that will be acked and filtered out of the pipeline.
type FilterRecord struct{}

func (FilterRecord) isProcessedRecord() {}

// ErrorRecord is a record that failed to be processed and will be nacked.
type ErrorRecord struct {
	// Err is the error cause.
	Err error
}

func (e ErrorRecord) isProcessedRecord() {}

// Support for MultiRecord will be added in the future.
// type MultiRecord []opencdc.Record
// func (MultiRecord) isProcessedRecord() {}
