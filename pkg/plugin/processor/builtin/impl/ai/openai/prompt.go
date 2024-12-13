// Copyright © 2024 Meroxa, Inc.
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

//go:generate paramgen -output=prompt_config_paramgen.go promptProcConfig

package openai

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/ai/azopenai"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/lang"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
)

const MetadataPromptContext = "weaviate.context"

type promptProcConfig struct {
	APIKey      string `json:"apiKey" validate:"required"`
	Endpoint    string `json:"endpoint" default:"https://api.openai.com/v1"`
	Prompt      string `json:"prompt" validate:"required"`
	InputField  string `json:"inputField" validate:"regex=^\\.(Payload|Key).*" default:".Payload.After"`
	OutputField string `json:"outputField" validate:"regex=^\\.(Payload|Key).*" default:".Payload.After"`
}

type promptProcessor struct {
	sdk.UnimplementedProcessor

	cfg promptProcConfig

	inputFieldRefResolver  sdk.ReferenceResolver
	outputFieldRefResolver sdk.ReferenceResolver

	client *azopenai.Client
	logger log.CtxLogger
}

func NewPromptProcessor(log log.CtxLogger) sdk.Processor {
	return &promptProcessor{
		logger: log.WithComponent("openai.prompt"),
	}
}

func (p *promptProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:        "openai.prompt",
		Summary:     "Generate OpenAI prompts.",
		Description: "",
		Version:     "v0.1.0",
		Author:      "Meroxa, Inc.",
		Parameters:  promptProcConfig{}.Parameters(),
	}, nil
}

func (p *promptProcessor) Configure(ctx context.Context, c config.Config) error {
	cfg := promptProcConfig{}
	err := sdk.ParseConfig(ctx, c, &cfg, promptProcConfig{}.Parameters())
	if err != nil {
		return cerrors.Errorf("failed to parse configuration: %w", err)
	}

	inputResolver, err := sdk.NewReferenceResolver(cfg.InputField)
	if err != nil {
		return cerrors.Errorf(`failed to create a field resolver for %v parameter: %w`, cfg.InputField, err)
	}
	p.inputFieldRefResolver = inputResolver

	outputResolver, err := sdk.NewReferenceResolver(cfg.OutputField)
	if err != nil {
		return cerrors.Errorf(`failed to create a field resolver for %v parameter: %w`, cfg.OutputField, err)
	}
	p.outputFieldRefResolver = outputResolver

	p.cfg = cfg
	return nil
}

func (p *promptProcessor) Open(ctx context.Context) error {
	keyCredential := azcore.NewKeyCredential(p.cfg.APIKey)

	// NOTE: this constructor creates a client that connects to the public OpenAI endpoint.
	// To connect to an Azure OpenAI endpoint, use azopenai.NewClient() or azopenai.NewClientWithyKeyCredential.
	client, err := azopenai.NewClientForOpenAI("https://api.openai.com/v1", keyCredential, nil)
	if err != nil {
		return cerrors.Errorf("failed to create OpenAI client: %w", err)
	}

	p.client = client
	return nil
}

func (p *promptProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))

	for _, rec := range records {
		out = append(out, p.processRecord(ctx, rec))
	}

	return out
}

func (p *promptProcessor) Teardown(ctx context.Context) error {
	return nil
}

func (p *promptProcessor) processRecord(ctx context.Context, rec opencdc.Record) sdk.ProcessedRecord {
	inRef, err := p.inputFieldRefResolver.Resolve(&rec)
	if err != nil {
		return sdk.ErrorRecord{
			Error: fmt.Errorf("failed to resolve reference %v: %w", p.cfg.InputField, err),
		}
	}

	var messages []azopenai.ChatRequestMessageClassification
	promptCtx, ok := rec.Metadata[MetadataPromptContext]
	if ok {
		messages = append(
			messages,
			&azopenai.ChatRequestSystemMessage{
				Content: azopenai.NewChatRequestSystemMessageContent(
					"Here's the context about the request that's going to be made: " + promptCtx,
				),
			},
		)
	}
	messages = append(
		messages,
		&azopenai.ChatRequestUserMessage{
			Content: azopenai.NewChatRequestUserMessageContent(p.cfg.Prompt + ": " + p.getPromptInput(inRef.Get())),
		},
	)

	resp, err := p.client.GetChatCompletions(
		ctx,
		azopenai.ChatCompletionsOptions{
			// This is a conversation in progress.
			// NOTE: all messages count against token usage for this API.
			Messages:       messages,
			DeploymentName: lang.Ptr("gpt-4o"),
		},
		nil,
	)
	if err != nil {
		return sdk.ErrorRecord{
			Error: fmt.Errorf("failed to get chat completions: %w", err),
		}
	}

	if resp.Choices[0].Message != nil && resp.Choices[0].Message.Content != nil {
		outRef, err := p.outputFieldRefResolver.Resolve(&rec)
		if err != nil {
			return sdk.ErrorRecord{
				Error: fmt.Errorf("failed to resolve reference %v: %w", p.cfg.OutputField, err),
			}
		}
		err = outRef.Set(*resp.Choices[0].Message.Content)
		if err != nil {
			return sdk.ErrorRecord{
				Error: fmt.Errorf("failed to set chat completion content for %v: %w", p.cfg.OutputField, err),
			}
		}
	}

	return sdk.SingleRecord(rec)
}

func (p *promptProcessor) getPromptInput(val any) string {
	switch v := val.(type) {
	case opencdc.RawData:
		return string(v)
	case opencdc.StructuredData:
		return string(v.Bytes())
	default:
		return fmt.Sprintf("%v", v)
	}
}
