// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package unwrap

import (
	"regexp"

	"github.com/conduitio/conduit-commons/config"
)

func (kafkaConnectConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		"field": {
			Default:     ".Payload.After",
			Description: "Field is a reference to the field that contains the Kafka Connect record.\n\nFor more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRegex{Regex: regexp.MustCompile("^.Payload")},
			},
		},
	}
}
