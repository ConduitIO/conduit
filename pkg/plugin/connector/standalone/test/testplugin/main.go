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

// Package main contains a plugin used for testing purposes.
package main

import (
	"context"
	"log"

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit-connector-protocol/cpluginv1/server"
)

// These constants need to match the constants in pkg/plugin/connector/standalone/registry_test.go
const (
	testPluginName        = "test-plugin"
	testPluginSummary     = "My test plugin summary"
	testPluginDescription = "My test plugin description"
	testPluginVersion     = "v0.1.0"
	testPluginAuthor      = "test"

	testPluginSourceParam1            = "src.foo"
	testPluginSourceParam1Default     = ""
	testPluginSourceParam1Description = "Required parameter"
	testPluginSourceParam2            = "src.bar"
	testPluginSourceParam2Default     = "bar"
	testPluginSourceParam2Required    = false
	testPluginSourceParam2Description = "Optional parameter"

	testPluginDestinationParam1            = "dest.foo"
	testPluginDestinationParam1Default     = ""
	testPluginDestinationParam1Required    = true
	testPluginDestinationParam1Description = "Required parameter"
	testPluginDestinationParam2            = "dest.bar"
	testPluginDestinationParam2Default     = "bar"
	testPluginDestinationParam2Required    = false
	testPluginDestinationParam2Description = "Optional parameter"

	testPluginValidationInclusionValue   = "one,two"
	testPluginValidationExclusionValue   = "3,4"
	testPluginValidationRegexValue       = "[1-9]"
	testPluginValidationLessThanValue    = "10"
	testPluginValidationGreaterThanValue = "1"
)

func main() {
	err := server.Serve(
		func() cpluginv1.SpecifierPlugin { return specifierPlugin{} },
		func() cpluginv1.SourcePlugin { return nil },
		func() cpluginv1.DestinationPlugin { return nil },
	)
	if err != nil {
		log.Fatal(err)
	}
}

type specifierPlugin struct{}

func (s specifierPlugin) Specify(context.Context, cpluginv1.SpecifierSpecifyRequest) (cpluginv1.SpecifierSpecifyResponse, error) {
	return cpluginv1.SpecifierSpecifyResponse{
		Name:        testPluginName,
		Summary:     testPluginSummary,
		Description: testPluginDescription,
		Version:     testPluginVersion,
		Author:      testPluginAuthor,
		SourceParams: map[string]cpluginv1.SpecifierParameter{
			testPluginSourceParam1: {
				Default:     testPluginSourceParam1Default,
				Description: testPluginSourceParam1Description,
				Validations: []cpluginv1.ParameterValidation{
					{
						Type: cpluginv1.ValidationTypeRequired,
					},
					{
						Type:  cpluginv1.ValidationTypeInclusion,
						Value: testPluginValidationInclusionValue,
					},
				},
			},
			testPluginSourceParam2: {
				Default:     testPluginSourceParam2Default,
				Required:    testPluginSourceParam2Required,
				Description: testPluginSourceParam2Description,
				Type:        cpluginv1.ParameterTypeInt,
				Validations: []cpluginv1.ParameterValidation{
					{
						Type:  cpluginv1.ValidationTypeExclusion,
						Value: testPluginValidationExclusionValue,
					},
					{
						Type:  cpluginv1.ValidationTypeGreaterThan,
						Value: testPluginValidationGreaterThanValue,
					},
				},
			},
		},
		DestinationParams: map[string]cpluginv1.SpecifierParameter{
			testPluginDestinationParam1: {
				Default:     testPluginDestinationParam1Default,
				Required:    testPluginDestinationParam1Required,
				Description: testPluginDestinationParam1Description,
				Type:        cpluginv1.ParameterTypeInt,
				Validations: []cpluginv1.ParameterValidation{
					{
						Type:  cpluginv1.ValidationTypeLessThan,
						Value: testPluginValidationLessThanValue,
					},
					{
						Type:  cpluginv1.ValidationTypeRegex,
						Value: testPluginValidationRegexValue,
					},
				},
			},
			testPluginDestinationParam2: {
				Default:     testPluginDestinationParam2Default,
				Required:    testPluginDestinationParam2Required,
				Description: testPluginDestinationParam2Description,
				Type:        cpluginv1.ParameterTypeDuration,
			},
		},
	}, nil
}
