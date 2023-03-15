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
	"github.com/conduitio/conduit/pkg/plugin"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var vTypes [1]struct{}
	_ = vTypes[int(plugin.ValidationTypeRequired)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_REQUIRED)]
	_ = vTypes[int(plugin.ValidationTypeGreaterThan)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_GREATER_THAN)]
	_ = vTypes[int(plugin.ValidationTypeLessThan)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_LESS_THAN)]
	_ = vTypes[int(plugin.ValidationTypeInclusion)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_INCLUSION)]
	_ = vTypes[int(plugin.ValidationTypeExclusion)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_EXCLUSION)]
	_ = vTypes[int(plugin.ValidationTypeRegex)-int(apiv1.PluginSpecifications_Parameter_Validation_TYPE_REGEX)]

	_ = vTypes[int(plugin.ParameterTypeString)-int(apiv1.PluginSpecifications_Parameter_TYPE_STRING)]
	_ = vTypes[int(plugin.ParameterTypeInt)-int(apiv1.PluginSpecifications_Parameter_TYPE_INT)]
	_ = vTypes[int(plugin.ParameterTypeFloat)-int(apiv1.PluginSpecifications_Parameter_TYPE_FLOAT)]
	_ = vTypes[int(plugin.ParameterTypeFile)-int(apiv1.PluginSpecifications_Parameter_TYPE_FILE)]
	_ = vTypes[int(plugin.ParameterTypeBool)-int(apiv1.PluginSpecifications_Parameter_TYPE_BOOL)]
	_ = vTypes[int(plugin.ParameterTypeDuration)-int(apiv1.PluginSpecifications_Parameter_TYPE_DURATION)]
}

func Plugin(name string, in plugin.Specification) *apiv1.PluginSpecifications {
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

func PluginParamsMap(in map[string]plugin.Parameter) map[string]*apiv1.PluginSpecifications_Parameter {
	out := make(map[string]*apiv1.PluginSpecifications_Parameter)
	for k, v := range in {
		out[k] = &apiv1.PluginSpecifications_Parameter{
			Description: v.Description,
			Default:     v.Default,
			Type:        apiv1.PluginSpecifications_Parameter_Type(v.Type),
			Validations: PluginParamValidations(v.Validations),
		}
	}
	return out
}

func PluginParamValidations(in []plugin.Validation) []*apiv1.PluginSpecifications_Parameter_Validation {
	// we need an empty slice here so that the returned JSON would be "validations":[] instead of "validations":null
	out := make([]*apiv1.PluginSpecifications_Parameter_Validation, 0)
	for _, v := range in {
		out = append(out, &apiv1.PluginSpecifications_Parameter_Validation{
			Type:  ValidationType(v.Type),
			Value: v.Value,
		})
	}
	return out
}

func ValidationType(in plugin.ValidationType) apiv1.PluginSpecifications_Parameter_Validation_Type {
	return apiv1.PluginSpecifications_Parameter_Validation_Type(in)
}
