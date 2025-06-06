// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package cohere

import (
	"github.com/conduitio/conduit-commons/config"
)

const (
	embedProcConfigApiKey             = "apiKey"
	embedProcConfigBackoffRetryCount  = "backoffRetry.count"
	embedProcConfigBackoffRetryFactor = "backoffRetry.factor"
	embedProcConfigBackoffRetryMax    = "backoffRetry.max"
	embedProcConfigBackoffRetryMin    = "backoffRetry.min"
	embedProcConfigInputField         = "inputField"
	embedProcConfigInputType          = "inputType"
	embedProcConfigMaxTextsPerRequest = "maxTextsPerRequest"
	embedProcConfigModel              = "model"
	embedProcConfigOutputField        = "outputField"
)

func (embedProcConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		embedProcConfigApiKey: {
			Default:     "",
			Description: "APIKey is the API key for Cohere api calls.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		embedProcConfigBackoffRetryCount: {
			Default:     "0",
			Description: "Maximum number of retries for an individual record when backing off following an error.",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{
				config.ValidationGreaterThan{V: -1},
			},
		},
		embedProcConfigBackoffRetryFactor: {
			Default:     "2",
			Description: "The multiplying factor for each increment step.",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{
				config.ValidationGreaterThan{V: 0},
			},
		},
		embedProcConfigBackoffRetryMax: {
			Default:     "5s",
			Description: "The maximum waiting time before retrying.",
			Type:        config.ParameterTypeDuration,
			Validations: []config.Validation{},
		},
		embedProcConfigBackoffRetryMin: {
			Default:     "100ms",
			Description: "The minimum waiting time before retrying.",
			Type:        config.ParameterTypeDuration,
			Validations: []config.Validation{},
		},
		embedProcConfigInputField: {
			Default:     ".Payload.After",
			Description: "Specifies the field from which the request body should be created.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		embedProcConfigInputType: {
			Default:     "",
			Description: "Specifies the type of input passed to the model. Required for embed models v3 and higher.\nAllowed values: search_document, search_query, classification, clustering, image.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		embedProcConfigMaxTextsPerRequest: {
			Default:     "96",
			Description: "MaxTextsPerRequest controls the number of texts sent in each Cohere embedding API call (max 96)",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		embedProcConfigModel: {
			Default:     "embed-english-v2.0",
			Description: "Model is one of the Cohere embed models.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		embedProcConfigOutputField: {
			Default:     ".Payload.After",
			Description: "OutputField specifies which field will the response body be saved at.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
	}
}
