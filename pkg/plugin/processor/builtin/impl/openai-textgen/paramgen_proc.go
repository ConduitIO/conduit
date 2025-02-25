// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package textgen

import (
	"github.com/conduitio/conduit-commons/config"
)

const (
	ProcessorConfigApiKey              = "api_key"
	ProcessorConfigDeveloperMessage    = "developer_message"
	ProcessorConfigField               = "field"
	ProcessorConfigFrequencyPenalty    = "frequency_penalty"
	ProcessorConfigLogProbs            = "log_probs"
	ProcessorConfigLogitBias           = "logit_bias.*"
	ProcessorConfigMaxCompletionTokens = "max_completion_tokens"
	ProcessorConfigMaxTokens           = "max_tokens"
	ProcessorConfigMetadata            = "metadata.*"
	ProcessorConfigModel               = "model"
	ProcessorConfigN                   = "n"
	ProcessorConfigPresencePenalty     = "presence_penalty"
	ProcessorConfigReasoningEffort     = "reasoning_effort"
	ProcessorConfigSeed                = "seed"
	ProcessorConfigStop                = "stop"
	ProcessorConfigStore               = "store"
	ProcessorConfigStream              = "stream"
	ProcessorConfigStrictOutput        = "strict_output"
	ProcessorConfigTemperature         = "temperature"
	ProcessorConfigTopLogProbs         = "top_log_probs"
	ProcessorConfigTopP                = "top_p"
	ProcessorConfigUser                = "user"
)

func (ProcessorConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		ProcessorConfigApiKey: {
			Default:     "",
			Description: "APIKey is the OpenAI API key. Required.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		ProcessorConfigDeveloperMessage: {
			Default:     "",
			Description: "DeveloperMessage is the system message that guides the model's behavior. Required.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		ProcessorConfigField: {
			Default:     ".Payload.After",
			Description: "Field is the reference to the field to process. Defaults to \".Payload.After\".",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		ProcessorConfigFrequencyPenalty: {
			Default:     "",
			Description: "FrequencyPenalty penalizes new tokens based on frequency in text.",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{},
		},
		ProcessorConfigLogProbs: {
			Default:     "",
			Description: "LogProbs is whether to return log probabilities of output tokens.",
			Type:        config.ParameterTypeBool,
			Validations: []config.Validation{},
		},
		ProcessorConfigLogitBias: {
			Default:     "",
			Description: "LogitBias modifies the likelihood of specified tokens appearing.",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		ProcessorConfigMaxCompletionTokens: {
			Default:     "",
			Description: "MaxCompletionTokens is the maximum number of tokens for completion.",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		ProcessorConfigMaxTokens: {
			Default:     "",
			Description: "MaxTokens is the maximum number of tokens to generate.",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		ProcessorConfigMetadata: {
			Default:     "",
			Description: "Metadata is additional metadata to include with the request.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		ProcessorConfigModel: {
			Default:     "",
			Description: "Model is the OpenAI model to use (e.g., gpt-4o-mini). Required.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
		ProcessorConfigN: {
			Default:     "",
			Description: "N is the number of completions to generate.",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		ProcessorConfigPresencePenalty: {
			Default:     "",
			Description: "PresencePenalty penalizes new tokens based on presence in text.",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{},
		},
		ProcessorConfigReasoningEffort: {
			Default:     "",
			Description: "ReasoningEffort controls the amount of reasoning in the response.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		ProcessorConfigSeed: {
			Default:     "",
			Description: "Seed is the seed for deterministic results.",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		ProcessorConfigStop: {
			Default:     "",
			Description: "Stop are sequences where the API will stop generating.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		ProcessorConfigStore: {
			Default:     "",
			Description: "Store is whether to store the conversation in OpenAI.",
			Type:        config.ParameterTypeBool,
			Validations: []config.Validation{},
		},
		ProcessorConfigStream: {
			Default:     "",
			Description: "Stream is whether to stream the results or not. Not used for now.",
			Type:        config.ParameterTypeBool,
			Validations: []config.Validation{},
		},
		ProcessorConfigStrictOutput: {
			Default:     "false",
			Description: "StrictOutput enforces strict output format. Defaults to false.",
			Type:        config.ParameterTypeBool,
			Validations: []config.Validation{},
		},
		ProcessorConfigTemperature: {
			Default:     "",
			Description: "Temperature controls randomness (0-2, lower is more deterministic).",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{},
		},
		ProcessorConfigTopLogProbs: {
			Default:     "",
			Description: "TopLogProbs is the number of most likely tokens to return probabilities for.",
			Type:        config.ParameterTypeInt,
			Validations: []config.Validation{},
		},
		ProcessorConfigTopP: {
			Default:     "",
			Description: "TopP controls diversity via nucleus sampling.",
			Type:        config.ParameterTypeFloat,
			Validations: []config.Validation{},
		},
		ProcessorConfigUser: {
			Default:     "",
			Description: "User is the user identifier for OpenAI API.",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
	}
}
