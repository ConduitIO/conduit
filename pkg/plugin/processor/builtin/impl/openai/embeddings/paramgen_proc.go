// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package embeddings

import (
	"github.com/conduitio/conduit-commons/config"
)

const (
	embeddingsProcessorConfigApiKey         = "api_key"
	embeddingsProcessorConfigBackoffFactor  = "backoff_factor"
	embeddingsProcessorConfigDimensions     = "dimensions"
	embeddingsProcessorConfigEncodingFormat = "encoding_format"
	embeddingsProcessorConfigField          = "field"
	embeddingsProcessorConfigInitialBackoff = "initial_backoff"
	embeddingsProcessorConfigMaxBackoff     = "max_backoff"
	embeddingsProcessorConfigMaxRetries     = "max_retries"
	embeddingsProcessorConfigModel          = "model"
	embeddingsProcessorConfigUser           = "user"
)

func (embeddingsProcessorConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		embeddingsProcessorConfigApiKey: {
			Default:     "",
			Description: "APIKey is the OpenAI API key.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		embeddingsProcessorConfigBackoffFactor: {
			Default:     "2.0",
			Description: "BackoffFactor is the factor by which the backoff increases. Defaults to 2.0",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{},
		},
		embeddingsProcessorConfigDimensions: {
			Default:     "",
			Description: "Dimensions is the number of dimensions the resulting output embeddings should have.",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		embeddingsProcessorConfigEncodingFormat: {
			Default:     "",
			Description: "EncodingFormat is the format to return the embeddings in. Can be \"float\" or \"base64\".",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		embeddingsProcessorConfigField: {
			Default:     ".Payload.After",
			Description: "Field is the reference to the field to process. Defaults to \".Payload.After\".",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		embeddingsProcessorConfigInitialBackoff: {
			Default:     "1000",
			Description: "InitialBackoff is the initial backoff duration in milliseconds. Defaults to 1000ms (1s).",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		embeddingsProcessorConfigMaxBackoff: {
			Default:     "30000",
			Description: "MaxBackoff is the maximum backoff duration in milliseconds. Defaults to 30000ms (30s).",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		embeddingsProcessorConfigMaxRetries: {
			Default:     "3",
			Description: "MaxRetries is the maximum number of retries for API calls. Defaults to 3.",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		embeddingsProcessorConfigModel: {
			Default:     "",
			Description: "Model is the OpenAI embeddings model to use (e.g., text-embedding-3-small).",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		embeddingsProcessorConfigUser: {
			Default:     "",
			Description: "User is the user identifier for OpenAI API.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
	}
}
