// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package cohere

import (
	"github.com/conduitio/conduit-commons/config"
)

const (
	commandProcessorConfigApiKey             = "apiKey"
	commandProcessorConfigBackoffRetryCount  = "backoffRetry.count"
	commandProcessorConfigBackoffRetryFactor = "backoffRetry.factor"
	commandProcessorConfigBackoffRetryMax    = "backoffRetry.max"
	commandProcessorConfigBackoffRetryMin    = "backoffRetry.min"
	commandProcessorConfigModel              = "model"
	commandProcessorConfigPrompt             = "prompt"
	commandProcessorConfigRequestBody        = "request.body"
	commandProcessorConfigResponseBody       = "response.body"
)

func (commandProcessorConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		commandProcessorConfigApiKey: {
			Default:     "",
			Description: "APIKey is the API key for Cohere api calls.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		commandProcessorConfigBackoffRetryCount: {
			Default:     "0",
			Description: "Maximum number of retries for an individual record when backing off following an error.",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{
				config.ValidationGreaterThan{V: -1},
			},
		},
		commandProcessorConfigBackoffRetryFactor: {
			Default:     "2",
			Description: "The multiplying factor for each increment step.",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{
				config.ValidationGreaterThan{V: 0},
			},
		},
		commandProcessorConfigBackoffRetryMax: {
			Default:     "5s",
			Description: "The maximum waiting time before retrying.",
			Type:        config.ParameterTypeDuration,
			Validations: []config.Validation{},
		},
		commandProcessorConfigBackoffRetryMin: {
			Default:     "100ms",
			Description: "The minimum waiting time before retrying.",
			Type:        config.ParameterTypeDuration,
			Validations: []config.Validation{},
		},
		commandProcessorConfigModel: {
			Default:     "command",
			Description: "Model is one of the name of a compatible command model version.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		commandProcessorConfigPrompt: {
			Default:     "",
			Description: "Prompt is the preset prompt.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		commandProcessorConfigRequestBody: {
			Default:     ".Payload.After",
			Description: "RequestBodyRef specifies the api request field.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		commandProcessorConfigResponseBody: {
			Default:     ".Payload.After",
			Description: "ResponseBodyRef specifies in which field should the response body be saved.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
	}
}
