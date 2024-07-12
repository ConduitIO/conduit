// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package avro

import (
	"github.com/conduitio/conduit-commons/config"
)

func (decodeConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		"auth.basic.password": {
			Default:     "",
			Description: "The password to use with basic authentication. This option is required if\nauth.basic.username contains a value. If both auth.basic.username and auth.basic.password\nare empty basic authentication is disabled.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		"auth.basic.username": {
			Default:     "",
			Description: "The username to use with basic authentication. This option is required if\nauth.basic.password contains a value. If both auth.basic.username and auth.basic.password\nare empty basic authentication is disabled.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		"field": {
			Default:     ".Payload.After",
			Description: "The field that will be decoded.\n\nFor more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		"tls.ca.cert": {
			Default:     "",
			Description: "The path to a file containing PEM encoded CA certificates. If this option is empty,\nConduit falls back to using the host's root CA set.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		"tls.client.cert": {
			Default:     "",
			Description: "The path to a file containing a PEM encoded certificate. This option is required\nif tls.client.key contains a value. If both tls.client.cert and tls.client.key are empty\nTLS is disabled.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		"tls.client.key": {
			Default:     "",
			Description: "The path to a file containing a PEM encoded private key. This option is required\nif tls.client.cert contains a value. If both tls.client.cert and tls.client.key are empty\nTLS is disabled.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		"url": {
			Default:     "",
			Description: "URL of the schema registry (e.g. http://localhost:8085)",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
	}
}
