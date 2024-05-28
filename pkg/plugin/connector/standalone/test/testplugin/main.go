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
	"regexp"
	"strings"

	"github.com/conduitio/conduit-commons/config"

	"github.com/conduitio/conduit-connector-protocol/cplugin"
	"github.com/conduitio/conduit-connector-protocol/cplugin/server"
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
	testPluginSourceParam2Description = "Optional parameter"

	testPluginDestinationParam1            = "dest.foo"
	testPluginDestinationParam1Default     = ""
	testPluginDestinationParam1Description = "Required parameter"
	testPluginDestinationParam2            = "dest.bar"
	testPluginDestinationParam2Default     = "bar"
	testPluginDestinationParam2Description = "Optional parameter"

	testPluginValidationInclusionValue   = "one,two"
	testPluginValidationExclusionValue   = "3,4"
	testPluginValidationRegexValue       = "[1-9]"
	testPluginValidationLessThanValue    = 10
	testPluginValidationGreaterThanValue = 1
)

func main() {
	err := server.Serve(
		func() cplugin.SpecifierPlugin { return specifierPlugin{} },
		func() cplugin.SourcePlugin { return nil },
		func() cplugin.DestinationPlugin { return nil },
	)
	if err != nil {
		log.Fatal(err)
	}
}

type specifierPlugin struct{}

func (s specifierPlugin) Specify(context.Context, cplugin.SpecifierSpecifyRequest) (cplugin.SpecifierSpecifyResponse, error) {
	return cplugin.SpecifierSpecifyResponse{
		Specification: cplugin.Specification{
			Name:        testPluginName,
			Summary:     testPluginSummary,
			Description: testPluginDescription,
			Version:     testPluginVersion,
			Author:      testPluginAuthor,
			SourceParams: config.Parameters{
				testPluginSourceParam1: {
					Default:     testPluginSourceParam1Default,
					Description: testPluginSourceParam1Description,
					Validations: []config.Validation{
						config.ValidationRequired{},
						config.ValidationInclusion{List: strings.Split(testPluginValidationInclusionValue, ",")},
					},
				},
				testPluginSourceParam2: {
					Default:     testPluginSourceParam2Default,
					Description: testPluginSourceParam2Description,
					Type:        config.ParameterTypeInt,
					Validations: []config.Validation{
						config.ValidationExclusion{List: strings.Split(testPluginValidationExclusionValue, ",")},
						config.ValidationGreaterThan{V: testPluginValidationGreaterThanValue},
					},
				},
			},
			DestinationParams: config.Parameters{
				testPluginDestinationParam1: {
					Default:     testPluginDestinationParam1Default,
					Description: testPluginDestinationParam1Description,
					Type:        config.ParameterTypeInt,
					Validations: []config.Validation{
						config.ValidationLessThan{V: testPluginValidationLessThanValue},
						config.ValidationRegex{Regex: regexp.MustCompile(testPluginValidationRegexValue)},
					},
				},
				testPluginDestinationParam2: {
					Default:     testPluginDestinationParam2Default,
					Description: testPluginDestinationParam2Description,
					Type:        config.ParameterTypeDuration,
				},
			},
		},
	}, nil
}
