// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package builtin

import (
	"regexp"

	"github.com/conduitio/conduit-commons/config"
)

func (unwrapOpenCDCConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		"field": {
			Default:     ".Payload.After",
			Description: "Filter is a reference to the field which contains the OpenCDC record.  For more information about record references, see: https://github.com/ConduitIO/conduit-processor-sdk/blob/cbdc5dcb5d3109f8f13b88624c9e360076b0bcdb/util.go#L66.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRegex{Regex: regexp.MustCompile("^.Payload")},
			},
		},
	}
}
