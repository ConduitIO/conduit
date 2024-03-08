// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package field

import (
	"regexp"

	"github.com/conduitio/conduit-commons/config"
)

func (convertConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		"field": {
			Default:     "",
			Description: "Field is the target field, as it would be addressed in a Go template (e.g. `.Payload.After.foo`).\nyou can only convert fields that are under `.Key` and `.Payload`, and said fields should contain structured data.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationRegex{Regex: regexp.MustCompile("^\\.(Payload|Key).*")},
			},
		},
		"type": {
			Default:     "",
			Description: "Type is the target field type after conversion, available options are: string, int, float, bool.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationInclusion{List: []string{"string", "int", "float", "bool"}},
			},
		},
	}
}
