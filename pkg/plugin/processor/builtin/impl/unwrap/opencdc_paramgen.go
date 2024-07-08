// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package unwrap

import (
	"github.com/conduitio/conduit-commons/config"
)

const (
	openCDCConfigField = "field"
)

func (openCDCConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		openCDCConfigField: {
			Default:     ".Payload.After",
			Description: "Field is a reference to the field that contains the OpenCDC record.\n\nFor more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
	}
}
