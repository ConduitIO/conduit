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

package fromplugin

import (
	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit/pkg/plugin"
)

func SpecifierSpecifyResponse(in cpluginv1.SpecifierSpecifyResponse) (plugin.Specification, error) {
	specMap := func(params map[string]cpluginv1.SpecifierParameter) map[string]plugin.Parameter {
		out := make(map[string]plugin.Parameter)
		for k, v := range params {
			out[k] = SpecifierParameter(v)
		}
		return out
	}

	return plugin.Specification{
		Name:              in.Name,
		Summary:           in.Summary,
		Description:       in.Description,
		Version:           in.Version,
		Author:            in.Author,
		DestinationParams: specMap(in.DestinationParams),
		SourceParams:      specMap(in.SourceParams),
	}, nil
}

func SpecifierParameter(in cpluginv1.SpecifierParameter) plugin.Parameter {
	var validations []plugin.Validation
	if in.Required {
		validations = append(validations, plugin.Validation{
			Type: plugin.ValidationTypeRequired,
		})
	}
	return plugin.Parameter{
		Default:     in.Default,
		Type:        "string",
		Description: in.Description,
		Validations: validations,
	}
}
