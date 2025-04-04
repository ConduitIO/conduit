// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package avro

import (
	"github.com/conduitio/conduit-commons/config"
)

const (
	decodeConfigField = "field"
)

func (decodeConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		decodeConfigField: {
			Default:     ".Payload.After",
			Description: "The field that will be decoded.\n\nFor more information about the format, see [Referencing fields](https://conduit.io/docs/using/processors/referencing-fields).",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
	}
}
