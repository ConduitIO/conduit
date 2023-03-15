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

package provisioning

import (
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/matryer/is"
)

func TestValidator_MandatoryFields(t *testing.T) {
	is := is.New(t)

	tests := []struct {
		name   string
		config PipelineConfig
	}{{
		name: "processor type is mandatory",
		config: PipelineConfig{
			Status:      "stopped",
			Name:        "pipeline1",
			Description: "desc1",
			Processors: map[string]ProcessorConfig{
				"pipeline1proc1": {
					// mandatory field
					Type: "",
					Settings: map[string]string{
						"additionalProp1": "string",
						"additionalProp2": "string",
					},
				},
			},
		},
	}, {
		name: "connector plugin is mandatory",
		config: PipelineConfig{
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: map[string]ConnectorConfig{
				"con1": {
					Type: "source",
					// mandatory field
					Plugin: "",
					Name:   "",
					Settings: map[string]string{
						"aws.region": "us-east-1",
						"aws.bucket": "my-bucket",
					},
				},
			},
		},
	}, {
		name: "connector type is mandatory",
		config: PipelineConfig{
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: map[string]ConnectorConfig{
				"con1": {
					// mandatory field
					Type:   "",
					Plugin: "builtin:s3",
					Name:   "",
					Settings: map[string]string{
						"aws.region": "us-east-1",
						"aws.bucket": "my-bucket",
					},
				},
			},
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePipelinesConfig(tt.config)
			is.True(err != nil)
			is.True(cerrors.Is(err, ErrMandatoryField))
		})
	}
}

func TestValidator_InvalidFields(t *testing.T) {
	is := is.New(t)

	tests := []struct {
		name   string
		config PipelineConfig
	}{{
		name: "connector type is invalid",
		config: PipelineConfig{
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: map[string]ConnectorConfig{
				"con1": {
					// invalid field
					Type:   "my-type",
					Plugin: "builtin:s3",
					Name:   "",
					Settings: map[string]string{
						"aws.region": "us-east-1",
						"aws.bucket": "my-bucket",
					},
				},
			},
		},
	}, {
		name: "pipeline status is invalid",
		config: PipelineConfig{
			// invalid field
			Status:      "invalid-status",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: map[string]ConnectorConfig{
				"con1": {
					Type:   "source",
					Plugin: "builtin:s3",
					Name:   "",
					Settings: map[string]string{
						"aws.region": "us-east-1",
						"aws.bucket": "my-bucket",
					},
				},
			},
		},
	}, {
		name: "processor workers is negative",
		config: PipelineConfig{
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Processors: map[string]ProcessorConfig{
				"proc1": {
					Type:     "js",
					Settings: map[string]string{},
					// invalid field
					Workers: -1,
				},
			},
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePipelinesConfig(tt.config)
			is.True(err != nil)
			is.True(cerrors.Is(err, ErrInvalidField))
		})
	}
}

func TestValidator_MultiErrors(t *testing.T) {
	is := is.New(t)

	before := PipelineConfig{
		Status:      "running",
		Name:        "pipeline1",
		Description: "desc1",
		Connectors: map[string]ConnectorConfig{
			"con1": {
				// invalid field #1
				Type: "my-type",
				// mandatory field #1
				Plugin: "",
				Name:   "",
				Settings: map[string]string{
					"aws.region": "us-east-1",
					"aws.bucket": "my-bucket",
				},
				Processors: map[string]ProcessorConfig{
					"pipeline1proc1": {
						// mandatory field #2
						Type: "",
						Settings: map[string]string{
							"additionalProp1": "string",
							"additionalProp2": "string",
						},
					},
				},
			},
		},
		Processors: map[string]ProcessorConfig{
			"pipeline1proc1": {
				// mandatory field #3
				Type: "",
				Settings: map[string]string{
					"additionalProp1": "string",
					"additionalProp2": "string",
				},
			},
		},
	}

	err := ValidatePipelinesConfig(before)

	var invalid, mandatory int
	var multierr *multierror.Error
	is.True(cerrors.As(err, &multierr))
	for _, gotErr := range multierr.Errors() {
		if cerrors.Is(gotErr, ErrInvalidField) {
			invalid++
		}
		if cerrors.Is(gotErr, ErrMandatoryField) {
			mandatory++
		}
	}
	is.Equal(len(multierr.Errors()), 4)
	is.Equal(invalid, 1)
	is.Equal(mandatory, 3)
}
