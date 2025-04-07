// Code generated by paramgen. DO NOT EDIT.
// Source: github.com/ConduitIO/conduit-commons/tree/main/paramgen

package ollama

import (
	"regexp"

	"github.com/conduitio/conduit-commons/config"
)

const (
	ollamaProcessorConfigField  = "field"
	ollamaProcessorConfigModel  = "model"
	ollamaProcessorConfigPrompt = "prompt"
	ollamaProcessorConfigUrl    = "url"
)

func (ollamaProcessorConfig) Parameters() map[string]config.Parameter {
	return map[string]config.Parameter{
		ollamaProcessorConfigField: {
			Default:     ".Payload.After",
			Description: "Field is a reference to the field that contains the Kafka Connect record.\n\nFor more information about the format, see [Referencing fields](https://conduit.io/docs/using/processors/referencing-fields).",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRegex{Regex: regexp.MustCompile("^.Payload")},
			},
		},
		ollamaProcessorConfigModel: {
			Default:     "llama3.2",
			Description: "Model is the name of the model used with ollama",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		ollamaProcessorConfigPrompt: {
			Default:     "",
			Description: "Prompt is the prompt to pass into ollama to tranform the data",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{},
		},
		ollamaProcessorConfigUrl: {
			Default:     "",
			Description: "OllamaURL is the url to the ollama instance",
			Type:        config.ParameterTypeString,
			Validations: []config.Validation{
				config.ValidationRequired{},
			},
		},
	}
}
