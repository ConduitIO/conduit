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

package fromproto

import (
	"fmt"

	"github.com/conduitio/conduit/pkg/plugin"
	connectorv1 "go.buf.build/grpc/go/conduitio/conduit-connector-protocol/connector/v1"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var vTypes [1]struct{}
	_ = vTypes[int(plugin.ValidationTypeRequired)-int(connectorv1.Specifier_Parameter_Validation_TYPE_REQUIRED)]
	_ = vTypes[int(plugin.ValidationTypeLessThan)-int(connectorv1.Specifier_Parameter_Validation_TYPE_LESS_THAN)]
	_ = vTypes[int(plugin.ValidationTypeGreaterThan)-int(connectorv1.Specifier_Parameter_Validation_TYPE_GREATER_THAN)]
	_ = vTypes[int(plugin.ValidationTypeInclusion)-int(connectorv1.Specifier_Parameter_Validation_TYPE_INCLUSION)]
	_ = vTypes[int(plugin.ValidationTypeExclusion)-int(connectorv1.Specifier_Parameter_Validation_TYPE_EXCLUSION)]
	_ = vTypes[int(plugin.ValidationTypeRegex)-int(connectorv1.Specifier_Parameter_Validation_TYPE_REGEX)]
	// parameter types
	_ = vTypes[int(plugin.ParameterTypeString)-int(connectorv1.Specifier_Parameter_TYPE_STRING)]
	_ = vTypes[int(plugin.ParameterTypeInt)-int(connectorv1.Specifier_Parameter_TYPE_INT)]
	_ = vTypes[int(plugin.ParameterTypeFloat)-int(connectorv1.Specifier_Parameter_TYPE_FLOAT)]
	_ = vTypes[int(plugin.ParameterTypeBool)-int(connectorv1.Specifier_Parameter_TYPE_BOOL)]
	_ = vTypes[int(plugin.ParameterTypeFile)-int(connectorv1.Specifier_Parameter_TYPE_FILE)]
	_ = vTypes[int(plugin.ParameterTypeDuration)-int(connectorv1.Specifier_Parameter_TYPE_DURATION)]
}

func SpecifierSpecifyResponse(in *connectorv1.Specifier_Specify_Response) (plugin.Specification, error) {
	specMap := func(in map[string]*connectorv1.Specifier_Parameter) (map[string]plugin.Parameter, error) {
		out := make(map[string]plugin.Parameter, len(in))
		var err error
		for k, v := range in {
			out[k], err = SpecifierParameter(v)
			if err != nil {
				return nil, fmt.Errorf("error converting SpecifierParameter %q: %w", k, err)
			}
		}
		return out, nil
	}

	sourceParams, err := specMap(in.SourceParams)
	if err != nil {
		return plugin.Specification{}, fmt.Errorf("error converting SourceSpec: %w", err)
	}

	destinationParams, err := specMap(in.DestinationParams)
	if err != nil {
		return plugin.Specification{}, fmt.Errorf("error converting DestinationSpec: %w", err)
	}

	out := plugin.Specification{
		Name:              in.Name,
		Summary:           in.Summary,
		Description:       in.Description,
		Version:           in.Version,
		Author:            in.Author,
		DestinationParams: destinationParams,
		SourceParams:      sourceParams,
	}
	return out, nil
}

func SpecifierParameter(in *connectorv1.Specifier_Parameter) (plugin.Parameter, error) {
	validations := make([]plugin.Validation, len(in.Validations))

	requiredExists := false
	for i, v := range in.Validations {
		validations[i] = plugin.Validation{
			Type:  plugin.ValidationType(v.Type),
			Value: v.Value,
		}
		if v.Type == connectorv1.Specifier_Parameter_Validation_TYPE_REQUIRED {
			requiredExists = true
		}
	}
	// making sure not to duplicate the required validation
	if in.Required && !requiredExists { //nolint: staticcheck // required is still supported for now
		validations = append(validations, plugin.Validation{ //nolint: makezero // list is full so need to append
			Type: plugin.ValidationTypeRequired,
		})
	}

	out := plugin.Parameter{
		Default:     in.Default,
		Description: in.Description,
		Type:        plugin.ParameterType(in.Type),
		Validations: validations,
	}
	return out, nil
}
