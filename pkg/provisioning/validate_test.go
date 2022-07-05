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

func TestValidator_ConnectorMandatoryField1(t *testing.T) {
	is := is.New(t)

	before := PipelineConfig{
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
	}

	err := ValidatePipelinesConfig(before)
	is.Equal(cerrors.Is(err, ErrMandatoryField), true)
}

func TestValidator_ConnectorMandatoryField2(t *testing.T) {
	is := is.New(t)

	before := PipelineConfig{
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
	}

	err := ValidatePipelinesConfig(before)
	is.Equal(cerrors.Is(err, ErrMandatoryField), true)
}

func TestValidator_ConnectorInvalidField(t *testing.T) {
	is := is.New(t)

	before := PipelineConfig{
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
	}

	err := ValidatePipelinesConfig(before)
	is.Equal(cerrors.Is(err, ErrInvalidField), true)
}

func TestValidator_ProcessorMandatoryField(t *testing.T) {
	is := is.New(t)

	before := PipelineConfig{
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
	}

	err := ValidatePipelinesConfig(before)
	is.Equal(cerrors.Is(err, ErrMandatoryField), true)
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
	is.Equal(invalid, 1)
	is.Equal(mandatory, 3)
}
