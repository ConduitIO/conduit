// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package field

import (
	"regexp"

	"github.com/conduitio/conduit-commons/config"
)

const (
	convertConfigField = "field"
	convertConfigType  = "type"
)

func (convertConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		convertConfigField: {
			Default:     "",
			Description: "Field is the target field that should be converted.\nNote that you can only convert fields in structured data under `.Key` and\n`.Payload`.\n\nFor more information about the format, see [Referencing fields](https://conduit.io/docs/using/processors/referencing-fields).",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationRegex{Regex: regexp.MustCompile("^\\.(Payload|Key).*")},
			},
		},
		convertConfigType: {
			Default:     "",
			Description: "Type is the target field type after conversion, available options are: `string`, `int`, `float`, `bool`, `time`.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationInclusion{List: []string{"string", "int", "float", "bool", "time"}},
			},
		},
	}
}
