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

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var vTypes [1]struct{}
	_ = vTypes[int(cpluginv1.ValidationTypeRequired)-int(plugin.ValidationTypeRequired)]
	_ = vTypes[int(cpluginv1.ValidationTypeLessThan)-int(plugin.ValidationTypeLessThan)]
	_ = vTypes[int(cpluginv1.ValidationTypeGreaterThan)-int(plugin.ValidationTypeGreaterThan)]
	_ = vTypes[int(cpluginv1.ValidationTypeInclusion)-int(plugin.ValidationTypeInclusion)]
	_ = vTypes[int(cpluginv1.ValidationTypeExclusion)-int(plugin.ValidationTypeExclusion)]
	_ = vTypes[int(cpluginv1.ValidationTypeRegex)-int(plugin.ValidationTypeRegex)]
	// parameter types
	_ = vTypes[int(cpluginv1.ParameterTypeString)-int(plugin.ParameterTypeString)]
	_ = vTypes[int(cpluginv1.ParameterTypeInt)-int(plugin.ParameterTypeInt)]
	_ = vTypes[int(cpluginv1.ParameterTypeFloat)-int(plugin.ParameterTypeFloat)]
	_ = vTypes[int(cpluginv1.ParameterTypeBool)-int(plugin.ParameterTypeBool)]
	_ = vTypes[int(cpluginv1.ParameterTypeFile)-int(plugin.ParameterTypeFile)]
	_ = vTypes[int(cpluginv1.ParameterTypeDuration)-int(plugin.ParameterTypeDuration)]
}

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
	validations := make([]plugin.Validation, len(in.Validations))

	requiredExists := false
	for i, v := range in.Validations {
		validations[i] = plugin.Validation{
			Type:  plugin.ValidationType(v.Type),
			Value: v.Value,
		}
		if v.Type == cpluginv1.ValidationTypeRequired {
			requiredExists = true
		}
	}
	// making sure not to duplicate the required validation
	if in.Required && !requiredExists {
		validations = append(validations, plugin.Validation{
			Type: plugin.ValidationTypeRequired,
		})
	}

	return plugin.Parameter{
		Default:     in.Default,
		Type:        cpluginv1ParamTypeToPluginParamType(in.Type),
		Description: in.Description,
		Validations: validations,
	}
}

func cpluginv1ParamTypeToPluginParamType(t cpluginv1.ParameterType) plugin.ParameterType {
	// default type should be string
	if t == 0 {
		return plugin.ParameterTypeString
	}
	return plugin.ParameterType(t)
}
