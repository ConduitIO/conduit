package textgen

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/sashabaranov/go-openai"
)

//go:generate go tool paramgen -output=paramgen_proc.go ProcessorConfig

type Processor struct {
	sdk.UnimplementedProcessor

	config            ProcessorConfig
	client            *openai.Client
	referenceResolver sdk.ReferenceResolver
}

type ProcessorConfig struct {
	// Field is the reference to the field to process. Defaults to ".Payload.After".
	Field string `json:"field" default:".Payload.After"`
	// ApiKey is the OpenAI API key. Required.
	ApiKey string `json:"api_key" validate:"required"`
	// DeveloperMessage is the system message that guides the model's behavior. Required.
	DeveloperMessage string `json:"developer_message" validate:"required"`
	// StrictOutput enforces strict output format. Defaults to false.
	StrictOutput bool `json:"strict_output" default:"false"`
	// Model is the OpenAI model to use (e.g., gpt-4o-mini). Required.
	Model string `json:"model" validate:"required"`
	// MaxTokens is the maximum number of tokens to generate.
	MaxTokens int `json:"max_tokens"`
	// MaxCompletionTokens is the maximum number of tokens for completion.
	MaxCompletionTokens int `json:"max_completion_tokens"`
	// Temperature controls randomness (0-2, lower is more deterministic).
	Temperature float32 `json:"temperature"`
	// TopP controls diversity via nucleus sampling.
	TopP float32 `json:"top_p"`
	// N is the number of completions to generate.
	N int `json:"n"`
	// Stream is whether to stream the results or not. Not used for now.
	Stream bool `json:"stream"`
	// Stop are sequences where the API will stop generating.
	Stop []string `json:"stop"`
	// PresencePenalty penalizes new tokens based on presence in text.
	PresencePenalty float32 `json:"presence_penalty"`
	// Seed is the seed for deterministic results.
	Seed *int `json:"seed"`
	// FrequencyPenalty penalizes new tokens based on frequency in text.
	FrequencyPenalty float32 `json:"frequency_penalty"`
	// LogitBias modifies the likelihood of specified tokens appearing.
	LogitBias map[string]int `json:"logit_bias"`
	// LogProbs is whether to return log probabilities of output tokens.
	LogProbs bool `json:"log_probs"`
	// TopLogProbs is the number of most likely tokens to return probabilities for.
	TopLogProbs int `json:"top_log_probs"`
	// User is the user identifier for OpenAI API.
	User string `json:"user"`
	// Store is whether to store the conversation in OpenAI.
	Store bool `json:"store"`
	// ReasoningEffort controls the amount of reasoning in the response.
	ReasoningEffort string `json:"reasoning_effort"`
	// Metadata is additional metadata to include with the request.
	Metadata map[string]string `json:"metadata"`
}

func NewProcessor() sdk.Processor {
	return sdk.ProcessorWithMiddleware(&Processor{}, sdk.DefaultProcessorMiddleware()...)
}

func (p *Processor) Configure(ctx context.Context, cfg config.Config) error {
	err := sdk.ParseConfig(ctx, cfg, &p.config, ProcessorConfig{}.Parameters())
	if err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	p.referenceResolver, err = sdk.NewReferenceResolver(p.config.Field)
	if err != nil {
		return fmt.Errorf("failed to create reference resolver: %w", err)
	}

	p.client = openai.NewClient(p.config.ApiKey)

	return nil
}

func (p *Processor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "openai-textgen",
		Summary:     "modify records using openai models",
		Description: "textgen is a conduit processor that will transform a record based on a given prompt",
		Version:     "devel",
		Author:      "Meroxa, Inc.",
		Parameters:  p.config.Parameters(),
	}, nil
}

func (p *Processor) Process(ctx context.Context, recs []opencdc.Record) []sdk.ProcessedRecord {
	processedRecords := make([]sdk.ProcessedRecord, len(recs))
	for i, rec := range recs {
		processed, err := p.processRecord(ctx, rec)
		if err != nil {
			processedRecords[i] = sdk.ErrorRecord{Error: err}
			continue
		}

		processedRecords[i] = sdk.SingleRecord(processed)
	}

	return processedRecords
}

func (p *Processor) processRecord(ctx context.Context, rec opencdc.Record) (opencdc.Record, error) {
	logger := sdk.Logger(ctx)

	ref, err := p.referenceResolver.Resolve(&rec)
	if err != nil {
		return rec, fmt.Errorf("failed to resolve reference: %w", err)
	}

	val := ref.Get()

	var payload string
	switch v := val.(type) {
	case opencdc.Position:
		payload = string(v)

		res, err := p.createChatCompletion(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create chat completion: %w", err)
		}

		logger.Trace().Msgf("processed record position %s", res)

		if err := ref.Set(opencdc.Position(res)); err != nil {
			return rec, fmt.Errorf("failed to set position: %w", err)
		}
	case opencdc.Data:
		payload = string(v.Bytes())

		res, err := p.createChatCompletion(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create chat completion: %w", err)
		}

		logger.Trace().Msgf("processed record data %s", res)

		var data opencdc.Data = opencdc.RawData(res)

		if err := ref.Set(data); err != nil {
			return rec, fmt.Errorf("failed to set data: %w", err)
		}

	case string:
		payload = v

		res, err := p.createChatCompletion(ctx, payload)
		if err != nil {
			return rec, fmt.Errorf("failed to create chat completion: %w", err)
		}

		logger.Trace().Msgf("processed record string %s", res)

		if err := ref.Set(res); err != nil {
			return rec, fmt.Errorf("failed to set data: %w", err)
		}
	default:
		return rec, fmt.Errorf("unsupported type %T", v)
	}

	return rec, nil
}

func (p *Processor) createChatCompletion(ctx context.Context, payload string) (string, error) {
	req := openai.ChatCompletionRequest{
		Model: p.config.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: "developer", Content: p.config.DeveloperMessage},
			{Role: "user", Content: payload},
		},
		MaxTokens:           p.config.MaxTokens,
		MaxCompletionTokens: p.config.MaxCompletionTokens,
		Temperature:         p.config.Temperature,
		TopP:                p.config.TopP,
		N:                   p.config.N,
		Stop:                p.config.Stop,
		PresencePenalty:     p.config.PresencePenalty,
		Seed:                p.config.Seed,
		FrequencyPenalty:    p.config.FrequencyPenalty,
		LogitBias:           p.config.LogitBias,
		LogProbs:            p.config.LogProbs,
		TopLogProbs:         p.config.TopLogProbs,
		User:                p.config.User,
		Store:               p.config.Store,
		ReasoningEffort:     p.config.ReasoningEffort,
	}

	res, err := p.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("chat completion failed: %w", err)
	}

	return res.Choices[0].Message.Content, nil
}
