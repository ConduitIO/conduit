// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package builtin

import (
	"github.com/conduitio/conduit-commons/config"
)

func (convertFieldConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		"field": {
			Default:     "",
			Description: "field The target field, as it would be addressed in a Go template.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		"type": {
			Default:     "",
			Description: "type The target field type after conversion.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationInclusion{List: []string{"string", "int", "float", "bool"}},
			},
		},
	}
}
