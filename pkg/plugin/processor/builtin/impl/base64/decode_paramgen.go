// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package base64

import (
	"github.com/conduitio/conduit-commons/config"
)

func (decodeConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		"field": {
			Default:     "",
			Description: "Field is the reference to the target field. Note that it is not allowed to\nbase64 decode the `.Position` field.\n\nFor more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
				config.ValidationExclusion{List: []string{".Position"}},
			},
		},
	}
}
