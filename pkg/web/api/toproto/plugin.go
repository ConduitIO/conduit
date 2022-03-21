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

func Plugin(in *plugin.Specification) *apiv1.PluginSpecifications {
	return &apiv1.PluginSpecifications{
		Name:              in.Name,
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
			Type:        apiv1.PluginSpecifications_Parameter_TYPE_STRING,
			Validations: PluginParamValidations(v.Validations),
		}
	}
	return out
}

func PluginParamValidations(in []plugin.Validation) []*apiv1.PluginSpecifications_Parameter_Validation {
	out := make([]*apiv1.PluginSpecifications_Parameter_Validation, 0)
	for _, v := range in {
		out = append(out, &apiv1.PluginSpecifications_Parameter_Validation{
			Type:  PluginValidationType(v.VType),
			Value: v.Value,
		})
	}
	return out
}

func PluginValidationType(in plugin.ValidationType) apiv1.PluginSpecifications_Parameter_Validation_Type {
	switch in {
	case plugin.ValidationTypeRequired:
		return apiv1.PluginSpecifications_Parameter_Validation_TYPE_REQUIRED
	case plugin.ValidationTypeGreaterThan:
		return apiv1.PluginSpecifications_Parameter_Validation_TYPE_GREATER_THAN
	case plugin.ValidationTypeLessThan:
		return apiv1.PluginSpecifications_Parameter_Validation_TYPE_LESS_THAN
	case plugin.ValidationTypeExclusion:
		return apiv1.PluginSpecifications_Parameter_Validation_TYPE_EXCLUSION
	case plugin.ValidationTypeInclusion:
		return apiv1.PluginSpecifications_Parameter_Validation_TYPE_INCLUSION
	case plugin.ValidationTypeRegex:
		return apiv1.PluginSpecifications_Parameter_Validation_TYPE_REGEX
	}

	return apiv1.PluginSpecifications_Parameter_Validation_TYPE_UNSPECIFIED
}
