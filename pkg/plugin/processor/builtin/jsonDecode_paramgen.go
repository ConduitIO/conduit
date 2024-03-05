// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package builtin

import (
	"github.com/conduitio/conduit-commons/config"
)

func (jsonDecodeConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		"field": {
			Default:     "",
			Description: "Field is the target field, as it would be addressed in a Go template (e.g. `.Payload.After`).\nApplicable values are `.Key`, `.Payload.Before` and `.Payload.After`, as they accept structured data format.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationInclusion{List: []string{".Key", ".Payload.Before", ".Payload.After"}},
			},
		},
	}
}
