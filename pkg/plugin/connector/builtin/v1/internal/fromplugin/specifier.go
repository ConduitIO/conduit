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
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var vTypes [1]struct{}
	_ = vTypes[int(cpluginv1.ValidationTypeRequired)-int(connectorPlugin.ValidationTypeRequired)]
	_ = vTypes[int(cpluginv1.ValidationTypeLessThan)-int(connectorPlugin.ValidationTypeLessThan)]
	_ = vTypes[int(cpluginv1.ValidationTypeGreaterThan)-int(connectorPlugin.ValidationTypeGreaterThan)]
	_ = vTypes[int(cpluginv1.ValidationTypeInclusion)-int(connectorPlugin.ValidationTypeInclusion)]
	_ = vTypes[int(cpluginv1.ValidationTypeExclusion)-int(connectorPlugin.ValidationTypeExclusion)]
	_ = vTypes[int(cpluginv1.ValidationTypeRegex)-int(connectorPlugin.ValidationTypeRegex)]
	// parameter types
	_ = vTypes[int(cpluginv1.ParameterTypeString)-int(connectorPlugin.ParameterTypeString)]
	_ = vTypes[int(cpluginv1.ParameterTypeInt)-int(connectorPlugin.ParameterTypeInt)]
	_ = vTypes[int(cpluginv1.ParameterTypeFloat)-int(connectorPlugin.ParameterTypeFloat)]
	_ = vTypes[int(cpluginv1.ParameterTypeBool)-int(connectorPlugin.ParameterTypeBool)]
	_ = vTypes[int(cpluginv1.ParameterTypeFile)-int(connectorPlugin.ParameterTypeFile)]
	_ = vTypes[int(cpluginv1.ParameterTypeDuration)-int(connectorPlugin.ParameterTypeDuration)]
}

func SpecifierSpecifyResponse(in cpluginv1.SpecifierSpecifyResponse) (connectorPlugin.Specification, error) {
	specMap := func(params map[string]cpluginv1.SpecifierParameter) map[string]connectorPlugin.Parameter {
		out := make(map[string]connectorPlugin.Parameter)
		for k, v := range params {
			out[k] = SpecifierParameter(v)
		}
		return out
	}

	return connectorPlugin.Specification{
		Name:              in.Name,
		Summary:           in.Summary,
		Description:       in.Description,
		Version:           in.Version,
		Author:            in.Author,
		DestinationParams: specMap(in.DestinationParams),
		SourceParams:      specMap(in.SourceParams),
	}, nil
}

func SpecifierParameter(in cpluginv1.SpecifierParameter) connectorPlugin.Parameter {
	validations := make([]connectorPlugin.Validation, len(in.Validations))

	requiredExists := false
	for i, v := range in.Validations {
		validations[i] = connectorPlugin.Validation{
			Type:  connectorPlugin.ValidationType(v.Type),
			Value: v.Value,
		}
		if v.Type == cpluginv1.ValidationTypeRequired {
			requiredExists = true
		}
	}
	//nolint:staticcheck // needed for backward compatibility
	// in.Required is converted to a validation of type ValidationTypeRequired
	// making sure not to duplicate the required validation
	if in.Required && !requiredExists {
		//nolint:makezero // we don't know upfront if we need add this validation
		validations = append(validations, connectorPlugin.Validation{
			Type: connectorPlugin.ValidationTypeRequired,
		})
	}

	return connectorPlugin.Parameter{
		Default:     in.Default,
		Type:        cpluginv1ParamTypeToPluginParamType(in.Type),
		Description: in.Description,
		Validations: validations,
	}
}

func cpluginv1ParamTypeToPluginParamType(t cpluginv1.ParameterType) connectorPlugin.ParameterType {
	// default type should be string
	if t == 0 {
		return connectorPlugin.ParameterTypeString
	}
	return connectorPlugin.ParameterType(t)
}
