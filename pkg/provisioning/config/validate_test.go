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

package config

import (
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/matryer/is"
)

func TestValidator_MandatoryFields(t *testing.T) {
	tests := []struct {
		name   string
		config Pipeline
	}{{
		name: "pipeline ID is mandatory",
		config: Pipeline{
			// mandatory field
			ID:          "",
			Status:      "stopped",
			Name:        "pipeline1",
			Description: "desc1",
		},
	}, {
		name: "processor type is mandatory",
		config: Pipeline{
			ID:          "pipeline1",
			Status:      "stopped",
			Name:        "pipeline1",
			Description: "desc1",
			Processors: []Processor{{
				ID: "pipeline1proc1",
				// mandatory field
				Plugin: "",
				Settings: map[string]string{
					"additionalProp1": "string",
					"additionalProp2": "string",
				},
			}},
		},
	}, {
		name: "connector plugin is mandatory",
		config: Pipeline{
			ID:          "pipeline1",
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: []Connector{{
				ID:   "con1",
				Type: "source",
				// mandatory field
				Plugin: "",
				Name:   "",
				Settings: map[string]string{
					"aws.region": "us-east-1",
					"aws.bucket": "my-bucket",
				},
			}},
		},
	}, {
		name: "connector type is mandatory",
		config: Pipeline{
			ID:          "pipeline1",
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: []Connector{{
				ID: "con1",
				// mandatory field
				Type:   "",
				Plugin: "builtin:s3",
				Name:   "",
				Settings: map[string]string{
					"aws.region": "us-east-1",
					"aws.bucket": "my-bucket",
				},
			}},
		},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			err := Validate(tt.config)
			is.True(err != nil)
			is.True(cerrors.Is(err, ErrMandatoryField))
		})
	}
}

func TestValidator_InvalidFields(t *testing.T) {
	tests := []struct {
		name    string
		config  Pipeline
		wantErr error
	}{{
		name: "connector type is invalid",
		config: Pipeline{
			ID:          "pipeline1",
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: []Connector{{
				ID: "con1",
				// invalid field
				Type:   "my-type",
				Plugin: "builtin:s3",
				Name:   "",
				Settings: map[string]string{
					"aws.region": "us-east-1",
					"aws.bucket": "my-bucket",
				},
			}},
		},
		wantErr: ErrInvalidField,
	}, {
		name: "connector id is too long",
		config: Pipeline{
			ID:          "pipeline1",
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: []Connector{{
				ID:     strings.Repeat("x", 257),
				Type:   "source",
				Plugin: "builtin:s3",
				Name:   "",
			}},
		},
		wantErr: connector.ErrIDOverLimit,
	}, {
		name: "connector name is too long",
		config: Pipeline{
			ID:          "pipeline1",
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: []Connector{{
				ID:     "x",
				Name:   strings.Repeat("x", 257),
				Type:   "source",
				Plugin: "builtin:s3",
			}},
		},
		wantErr: connector.ErrNameOverLimit,
	}, {
		name: "pipeline id is too long",
		config: Pipeline{
			ID:          strings.Repeat("x", 257),
			Status:      "running",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors:  []Connector{},
		},
		wantErr: pipeline.ErrIDOverLimit,
	}, {
		name: "pipeline name is too long",
		config: Pipeline{
			ID:          "pipeline1",
			Name:        strings.Repeat("x", 257),
			Status:      "running",
			Description: "desc1",
			Connectors:  []Connector{},
		},
		wantErr: pipeline.ErrNameOverLimit,
	}, {
		name: "pipeline desc is too long",
		config: Pipeline{
			ID:          "pipeline1",
			Name:        "pipeline1",
			Status:      "running",
			Description: strings.Repeat("x", 9000),
			Connectors:  []Connector{},
		},
		wantErr: pipeline.ErrDescriptionOverLimit,
	}, {
		name: "pipeline status is invalid",
		config: Pipeline{
			ID: "pipeline1",
			// invalid field
			Status:      "invalid-status",
			Name:        "pipeline1",
			Description: "desc1",
			Connectors: []Connector{{
				ID:     "con1",
				Type:   "source",
				Plugin: "builtin:s3",
				Name:   "",
				Settings: map[string]string{
					"aws.region": "us-east-1",
					"aws.bucket": "my-bucket",
				},
			}},
		},
		wantErr: ErrInvalidField,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			err := Validate(tt.config)
			is.True(err != nil)
			is.True(cerrors.Is(err, tt.wantErr))
		})
	}
}

func TestValidator_MultiErrors(t *testing.T) {
	is := is.New(t)

	before := Pipeline{
		ID:          "pipeline1",
		Status:      "running",
		Name:        "pipeline1",
		Description: "desc1",
		Connectors: []Connector{{
			ID: "con1",
			// invalid field #1
			Type: "my-type",
			// mandatory field #1
			Plugin: "",
			Name:   "",
			Settings: map[string]string{
				"aws.region": "us-east-1",
				"aws.bucket": "my-bucket",
			},
			Processors: []Processor{{
				ID: "pipeline1proc1",
				// mandatory field #2
				Plugin: "",
				Settings: map[string]string{
					"additionalProp1": "string",
					"additionalProp2": "string",
				},
			}},
		}},
		Processors: []Processor{{
			ID: "pipeline1proc1",
			// mandatory field #3
			Plugin: "",
			Settings: map[string]string{
				"additionalProp1": "string",
				"additionalProp2": "string",
			},
		}},
	}

	err := Validate(before)

	var invalid, mandatory, all int
	cerrors.ForEach(err, func(err error) {
		all++
		if cerrors.Is(err, ErrInvalidField) {
			invalid++
		}
		if cerrors.Is(err, ErrMandatoryField) {
			mandatory++
		}
	})
	is.Equal(all, 4)
	is.Equal(invalid, 1)
	is.Equal(mandatory, 3)
}

func TestValidator_DuplicateID(t *testing.T) {
	is := is.New(t)

	test := Pipeline{
		ID:          "pipeline1",
		Status:      "running",
		Name:        "pipeline1",
		Description: "desc1",
		Connectors: []Connector{
			{
				ID:     "con1",
				Type:   "source",
				Plugin: "builtin:s3",
				Name:   "",
				Settings: map[string]string{
					"aws.region": "us-east-1",
					"aws.bucket": "my-bucket",
				},
				Processors: []Processor{
					{
						ID:     "pipeline1proc1",
						Plugin: "js",
						Settings: map[string]string{
							"additionalProp1": "string",
							"additionalProp2": "string",
						},
					},
				},
			},
			{
				ID:     "con1",
				Type:   "destination",
				Plugin: "builtin:s3",
				Name:   "",
				Settings: map[string]string{
					"aws.region": "us-east-1",
					"aws.bucket": "my-bucket",
				},
				Processors: []Processor{
					{
						ID:     "pipeline1proc1",
						Plugin: "js",
						Settings: map[string]string{
							"additionalProp1": "string",
							"additionalProp2": "string",
						},
					},
				},
			},
		},
		Processors: []Processor{
			{
				ID:     "pipeline1proc1",
				Plugin: "js",
				Settings: map[string]string{
					"additionalProp1": "string",
					"additionalProp2": "string",
				},
			},
		},
	}

	err := Validate(test)
	is.True(err != nil)
	is.True(cerrors.Is(err, ErrDuplicateID))
}
