// Copyright © 2025 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cohere

import (
	"context"
	"fmt"
	"time"

	cohere "github.com/cohere-ai/cohere-go/v2"
	cohereClient "github.com/cohere-ai/cohere-go/v2/client"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/jpillora/backoff"
)

type commandModel interface {
	Command(ctx context.Context, content string) (string, error)
}

type commandClient struct {
	client *cohereClient.Client
	config *commandProcessorConfig
}

//go:generate paramgen -output=paramgen_command.go commandProcessorConfig

type commandProcessor struct {
	sdk.UnimplementedProcessor

	requestBodyRef  *sdk.ReferenceResolver
	responseBodyRef *sdk.ReferenceResolver
	logger          log.CtxLogger
	config          commandProcessorConfig
	backoffCfg      *backoff.Backoff
	client          commandModel
}

type commandProcessorConfig struct {
	// Model is one of the name of a compatible command model version.
	Model string `json:"model" default:"command"`
	// APIKey is the API key for Cohere api calls.
	APIKey string `json:"apiKey" validate:"required"`
	// Maximum number of retries for an individual record when backing off following an error.
	BackoffRetryCount float64 `json:"backoffRetry.count" default:"0" validate:"gt=-1"`
	// The multiplying factor for each increment step.
	BackoffRetryFactor float64 `json:"backoffRetry.factor" default:"2" validate:"gt=0"`
	// The minimum waiting time before retrying.
	BackoffRetryMin time.Duration `json:"backoffRetry.min" default:"100ms"`
	// The maximum waiting time before retrying.
	BackoffRetryMax time.Duration `json:"backoffRetry.max" default:"5s"`
	// Prompt is the preset prompt.
	Prompt string `json:"prompt" validate:"required"`
	// RequestBodyRef specifies the api request field.
	RequestBodyRef string `json:"request.body" default:".Payload.After"`
	// ResponseBodyRef specifies in which field should the response body be saved.
	ResponseBodyRef string `json:"response.body" default:".Payload.After"`
}

func NewCommandProcessor(l log.CtxLogger) sdk.Processor {
	return &commandProcessor{logger: l.WithComponent("cohere.command")}
}

func (p *commandProcessor) Configure(ctx context.Context, cfg config.Config) error {
	// Configure is the first function to be called in a processor. It provides the processor
	// with the configuration that needs to be validated and stored to be used in other methods.
	// This method should not open connections or any other resources. It should solely focus
	// on parsing and validating the configuration itself.

	err := sdk.ParseConfig(ctx, cfg, &p.config, commandProcessorConfig{}.Parameters())
	if err != nil {
		return fmt.Errorf("failed to parse configuration: %w", err)
	}

	requestBodyRef, err := sdk.NewReferenceResolver(p.config.RequestBodyRef)
	if err != nil {
		return fmt.Errorf("failed parsing request.body %v: %w", p.config.RequestBodyRef, err)
	}
	p.requestBodyRef = &requestBodyRef

	responseBodyRef, err := sdk.NewReferenceResolver(p.config.ResponseBodyRef)
	if err != nil {
		return fmt.Errorf("failed parsing response.body %v: %w", p.config.ResponseBodyRef, err)
	}
	p.responseBodyRef = &responseBodyRef

	p.client = &commandClient{
		client: cohereClient.NewClient(),
		config: &p.config,
	}

	p.backoffCfg = &backoff.Backoff{
		Factor: p.config.BackoffRetryFactor,
		Min:    p.config.BackoffRetryMin,
		Max:    p.config.BackoffRetryMax,
	}

	return nil
}

func (p *commandProcessor) Specification() (sdk.Specification, error) {
	// Specification contains the metadata for the processor, which can be used to define how
	// to reference the processor, describe what the processor does and the configuration
	// parameters it expects.

	return sdk.Specification{
		Name:        "cohere.command",
		Summary:     "Conduit processor for Cohere's command model.",
		Description: "Conduit processor for Cohere's command model.",
		Version:     "v0.1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  p.config.Parameters(),
	}, nil
}

func (p *commandProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		var key []byte
		if record.Key != nil {
			key = record.Key.Bytes()
		}
		p.logger.Trace(ctx).Bytes("record_key", key).Msg("processing record")

		requestRef, err := p.requestBodyRef.Resolve(&record)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: fmt.Errorf("failed to resolve reference %v: %w", p.config.RequestBodyRef, err)})
		}

		content := fmt.Sprintf(p.config.Prompt, p.getInput(requestRef.Get()))
		for {
			resp, err := p.client.Command(ctx, content)
			attempt := p.backoffCfg.Attempt()
			duration := p.backoffCfg.Duration()

			if err != nil {
				switch {
				case cerrors.As(err, &cohere.GatewayTimeoutError{}),
					cerrors.As(err, &cohere.InternalServerError{}),
					cerrors.As(err, &cohere.ServiceUnavailableError{}):

					if attempt < p.config.BackoffRetryCount {
						sdk.Logger(ctx).Debug().Err(err).Float64("attempt", attempt).
							Float64("backoffRetry.count", p.config.BackoffRetryCount).
							Int64("backoffRetry.duration", duration.Milliseconds()).
							Msg("retrying Cohere HTTP request")

						select {
						case <-ctx.Done():
							return append(out, sdk.ErrorRecord{Error: ctx.Err()})
						case <-time.After(duration):
							continue
						}
					} else {
						return append(out, sdk.ErrorRecord{Error: err})
					}

				default:
					// BadRequestError, ClientClosedRequestError, ForbiddenError, InvalidTokenError,
					// NotFoundError, NotImplementedError, TooManyRequestsError, UnauthorizedError, UnprocessableEntityError
					return append(out, sdk.ErrorRecord{Error: err})
				}
			}

			p.backoffCfg.Reset()

			err = p.setField(&record, p.responseBodyRef, resp)
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: fmt.Errorf("failed setting response body: %w", err)})
			}
			out = append(out, sdk.SingleRecord(record))

			break
		}
	}
	return out
}

func (cc *commandClient) Command(ctx context.Context, content string) (string, error) {
	resp, err := cc.client.V2.Chat(
		ctx,
		&cohere.V2ChatRequest{
			Model: cc.config.Model,
			Messages: cohere.ChatMessages{
				{
					Role: "user",
					User: &cohere.UserMessage{Content: &cohere.UserMessageContent{
						String: content,
					}},
				},
			},
		},
		cohereClient.WithToken(cc.config.APIKey),
	)
	if err != nil {
		return "", err
	}

	chatResponse, err := unmarshalChatResponse([]byte(resp.String()))
	if err != nil {
		return "", fmt.Errorf("error unmarshalling chat response: %w", err)
	}

	if len(chatResponse.Message.Content) != 1 {
		return "", fmt.Errorf("invalid chat content")
	}

	return chatResponse.Message.Content[0].Text, nil
}

type ChatResponse struct {
	ID           string `json:"id"`
	FinishReason string `json:"finish_reason"`
	Message      struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func unmarshalChatResponse(res []byte) (*ChatResponse, error) {
	response := &ChatResponse{}
	err := json.Unmarshal(res, response)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (p *commandProcessor) getInput(val any) string {
	switch v := val.(type) {
	case opencdc.RawData:
		return string(v)
	case opencdc.StructuredData:
		return string(v.Bytes())
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (p *commandProcessor) setField(r *opencdc.Record, refRes *sdk.ReferenceResolver, data any) error {
	if refRes == nil {
		return nil
	}

	ref, err := refRes.Resolve(r)
	if err != nil {
		return fmt.Errorf("error reference resolver: %w", err)
	}

	err = ref.Set(data)
	if err != nil {
		return fmt.Errorf("error reference set: %w", err)
	}

	return nil
}
