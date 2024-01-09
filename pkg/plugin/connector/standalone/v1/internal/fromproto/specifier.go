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

	connectorv1 "github.com/conduitio/conduit-connector-protocol/proto/connector/v1"
	"github.com/conduitio/conduit/pkg/plugin/connector"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var vTypes [1]struct{}
	_ = vTypes[int(connector.ValidationTypeRequired)-int(connectorv1.Specifier_Parameter_Validation_TYPE_REQUIRED)]
	_ = vTypes[int(connector.ValidationTypeLessThan)-int(connectorv1.Specifier_Parameter_Validation_TYPE_LESS_THAN)]
	_ = vTypes[int(connector.ValidationTypeGreaterThan)-int(connectorv1.Specifier_Parameter_Validation_TYPE_GREATER_THAN)]
	_ = vTypes[int(connector.ValidationTypeInclusion)-int(connectorv1.Specifier_Parameter_Validation_TYPE_INCLUSION)]
	_ = vTypes[int(connector.ValidationTypeExclusion)-int(connectorv1.Specifier_Parameter_Validation_TYPE_EXCLUSION)]
	_ = vTypes[int(connector.ValidationTypeRegex)-int(connectorv1.Specifier_Parameter_Validation_TYPE_REGEX)]
	// parameter types
	_ = vTypes[int(connector.ParameterTypeString)-int(connectorv1.Specifier_Parameter_TYPE_STRING)]
	_ = vTypes[int(connector.ParameterTypeInt)-int(connectorv1.Specifier_Parameter_TYPE_INT)]
	_ = vTypes[int(connector.ParameterTypeFloat)-int(connectorv1.Specifier_Parameter_TYPE_FLOAT)]
	_ = vTypes[int(connector.ParameterTypeBool)-int(connectorv1.Specifier_Parameter_TYPE_BOOL)]
	_ = vTypes[int(connector.ParameterTypeFile)-int(connectorv1.Specifier_Parameter_TYPE_FILE)]
	_ = vTypes[int(connector.ParameterTypeDuration)-int(connectorv1.Specifier_Parameter_TYPE_DURATION)]
}

func SpecifierSpecifyResponse(in *connectorv1.Specifier_Specify_Response) (connector.Specification, error) {
	specMap := func(in map[string]*connectorv1.Specifier_Parameter) (map[string]connector.Parameter, error) {
		out := make(map[string]connector.Parameter, len(in))
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
		return connector.Specification{}, fmt.Errorf("error converting SourceSpec: %w", err)
	}

	destinationParams, err := specMap(in.DestinationParams)
	if err != nil {
		return connector.Specification{}, fmt.Errorf("error converting DestinationSpec: %w", err)
	}

	out := connector.Specification{
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

func SpecifierParameter(in *connectorv1.Specifier_Parameter) (connector.Parameter, error) {
	validations := make([]connector.Validation, len(in.Validations))

	requiredExists := false
	for i, v := range in.Validations {
		validations[i] = connector.Validation{
			Type:  connector.ValidationType(v.Type),
			Value: v.Value,
		}
		if v.Type == connectorv1.Specifier_Parameter_Validation_TYPE_REQUIRED {
			requiredExists = true
		}
	}
	// needed for backward compatibility, in.Required is converted to a validation of type ValidationTypeRequired
	// making sure not to duplicate the required validation
	if in.Required && !requiredExists { //nolint: staticcheck // required is still supported for now
		validations = append(validations, connector.Validation{ //nolint: makezero // list is full so need to append
			Type: connector.ValidationTypeRequired,
		})
	}

	out := connector.Parameter{
		Default:     in.Default,
		Description: in.Description,
		Type:        connectorv1ParamTypeToPluginParamType(in.Type),
		Validations: validations,
	}
	return out, nil
}

func connectorv1ParamTypeToPluginParamType(t connectorv1.Specifier_Parameter_Type) connector.ParameterType {
	// default type should be string
	if t == connectorv1.Specifier_Parameter_TYPE_UNSPECIFIED {
		return connector.ParameterTypeString
	}
	return connector.ParameterType(t)
}
