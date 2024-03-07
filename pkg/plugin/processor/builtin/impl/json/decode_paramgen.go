// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package json

import (
	"regexp"

	"github.com/conduitio/conduit-commons/config"
)

func (decodeConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		"field": {
			Default:     "",
			Description: "Field is the target field, as it would be addressed in a Go template (e.g. `.Payload.After.foo`).\nyou can only decode fields that are under `.Key` and `.Payload`.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationRegex{Regex: regexp.MustCompile("^\\.(Payload|Key).*")},
				config.ValidationExclusion{List: []string{".Payload"}},
			},
		},
	}
}
