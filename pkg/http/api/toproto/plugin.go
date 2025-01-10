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

package toproto

import (
	"github.com/conduitio/conduit-commons/config"
	configv1 "github.com/conduitio/conduit-commons/proto/config/v1"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	processorSdk "github.com/conduitio/conduit-processor-sdk"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var vTypes [1]struct{}
	_ = vTypes[int(config.ValidationTypeRequired)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_REQUIRED)]
	_ = vTypes[int(config.ValidationTypeGreaterThan)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_GREATER_THAN)]
	_ = vTypes[int(config.ValidationTypeLessThan)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_LESS_THAN)]
	_ = vTypes[int(config.ValidationTypeInclusion)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_INCLUSION)]
	_ = vTypes[int(config.ValidationTypeExclusion)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_EXCLUSION)]
	_ = vTypes[int(config.ValidationTypeRegex)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_REGEX)]

	_ = vTypes[int(config.ParameterTypeString)-int(apiv1.PluginSpecifications_Parameter_TYPE_STRING)]
	_ = vTypes[int(config.ParameterTypeInt)-int(apiv1.PluginSpecifications_Parameter_TYPE_INT)]
	_ = vTypes[int(config.ParameterTypeFloat)-int(apiv1.PluginSpecifications_Parameter_TYPE_FLOAT)]
	_ = vTypes[int(config.ParameterTypeFile)-int(apiv1.PluginSpecifications_Parameter_TYPE_FILE)]
	_ = vTypes[int(config.ParameterTypeBool)-int(apiv1.PluginSpecifications_Parameter_TYPE_BOOL)]
	_ = vTypes[int(config.ParameterTypeDuration)-int(apiv1.PluginSpecifications_Parameter_TYPE_DURATION)]
}

// Deprecated: this is here for backwards compatibility with the old plugin API.
func PluginSpecifications(name string, in pconnector.Specification) *apiv1.PluginSpecifications {
	return &apiv1.PluginSpecifications{
		Name:              name,
		Summary:           in.Summary,
		Description:       in.Description,
		Version:           in.Version,
		Author:            in.Author,
		DestinationParams: PluginParamsMap(in.DestinationParams),
		SourceParams:      PluginParamsMap(in.SourceParams),
	}
}

// Deprecated: this is here for backwards compatibility with the old plugin API.
func PluginParamsMap(in map[string]config.Parameter) map[string]*apiv1.PluginSpecifications_Parameter {
	out := make(map[string]*apiv1.PluginSpecifications_Parameter)
	for k, v := range in {
		out[k] = &apiv1.PluginSpecifications_Parameter{
			Description: v.Description,
			Default:     v.Default,
			Type:        apiv1.PluginSpecifications_Parameter_Type(v.Type), //nolint:gosec // deprecated
			Validations: PluginParamValidations(v.Validations),
		}
	}
	return out
}

// Deprecated: this is here for backwards compatibility with the old plugin API.
func PluginParamValidations(in []config.Validation) []*apiv1.PluginSpecifications_Parameter_Validation {
	// we need an empty slice here so that the returned JSON would be "validations":[] instead of "validations":null
	out := make([]*apiv1.PluginSpecifications_Parameter_Validation, 0)
	for _, v := range in {
		out = append(out, &apiv1.PluginSpecifications_Parameter_Validation{
			Type:  apiv1.PluginSpecifications_Parameter_Validation_Type(v.Type()), //nolint:gosec // deprecated
			Value: v.Value(),
		})
	}
	return out
}

func ConnectorPluginSpecifications(name string, in pconnector.Specification) *apiv1.ConnectorPluginSpecifications {
	return &apiv1.ConnectorPluginSpecifications{
		Name:              name,
		Summary:           in.Summary,
		Description:       in.Description,
		Version:           in.Version,
		DestinationParams: ConnectorPluginParamsMap(in.DestinationParams),
		SourceParams:      ConnectorPluginParamsMap(in.SourceParams),
	}
}

func ConnectorPluginParamsMap(in map[string]config.Parameter) map[string]*configv1.Parameter {
	out := make(map[string]*configv1.Parameter)
	for k, v := range in {
		out[k] = &configv1.Parameter{
			Description: v.Description,
			Default:     v.Default,
			Type:        configv1.Parameter_Type(v.Type), //nolint:gosec // no risk of overflow
			Validations: ConnectorPluginParamValidations(v.Validations),
		}
	}
	return out
}

func ConnectorPluginParamValidations(in []config.Validation) []*configv1.Validation {
	// we need an empty slice here so that the returned JSON would be "validations":[] instead of "validations":null
	out := make([]*configv1.Validation, 0)
	for _, v := range in {
		out = append(out, &configv1.Validation{
			Type:  configv1.Validation_Type(v.Type()), //nolint:gosec // no risk of overflow
			Value: v.Value(),
		})
	}
	return out
}

func ProcessorPluginSpecifications(name string, in processorSdk.Specification) *apiv1.ProcessorPluginSpecifications {
	params := make(map[string]*configv1.Parameter)
	in.Parameters.ToProto(params)
	return &apiv1.ProcessorPluginSpecifications{
		Name:        name,
		Summary:     in.Summary,
		Description: in.Description,
		Version:     in.Version,
		Parameters:  params,
	}
}
