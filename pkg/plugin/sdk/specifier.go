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

package sdk

import (
	"context"

	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
)

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

func NewSpecifierPlugin(specs Specification) cpluginv1.SpecifierPlugin {
	return &specifierPluginAdapter{specs: specs}
}

type specifierPluginAdapter struct {
	specs Specification
}

func (s *specifierPluginAdapter) Specify(ctx context.Context, req cpluginv1.SpecifierSpecifyRequest) (cpluginv1.SpecifierSpecifyResponse, error) {
	return cpluginv1.SpecifierSpecifyResponse{
		Name:              s.specs.Name,
		Summary:           s.specs.Summary,
		Description:       s.specs.Description,
		Version:           s.specs.Version,
		Author:            s.specs.Author,
		DestinationParams: s.convertParameters(s.specs.DestinationParams),
		SourceParams:      s.convertParameters(s.specs.SourceParams),
	}, nil
}

func (s *specifierPluginAdapter) convertParameters(params map[string]Parameter) map[string]cpluginv1.SpecifierParameter {
	out := make(map[string]cpluginv1.SpecifierParameter)
	for k, v := range params {
		out[k] = s.convertParameter(v)
	}
	return out
}

func (s *specifierPluginAdapter) convertParameter(p Parameter) cpluginv1.SpecifierParameter {
	return cpluginv1.SpecifierParameter{
		Default:     p.Default,
		Required:    p.Required,
		Description: p.Description,
	}
}
