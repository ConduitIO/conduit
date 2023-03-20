// Copyright © 2023 Meroxa, Inc.
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

package yaml

import (
	"context"
	"io"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/yaml/v3"
)

type Parser struct {
	logger log.CtxLogger
}

func NewParser(logger log.CtxLogger) *Parser {
	return &Parser{
		logger: logger.WithComponent("yaml.Parser"),
	}
}

func (p *Parser) Parse(ctx context.Context, reader io.Reader) ([]config.Pipeline, error) {
	configs, err := p.ParseConfiguration(ctx, reader)
	if err != nil {
		return nil, err
	}

	return configs.ToConfig(), nil
}

func (p *Parser) ParseConfiguration(ctx context.Context, reader io.Reader) (Configurations, error) {
	dec := yaml.NewDecoder(reader)
	dec.KnownFields(true)

	var configs Configurations
	for {
		var cfg Configuration
		linter := newConfigLinter()
		dec.WithHook(multiDecoderHook(
			envDecoderHook,     // replace environment variables with their values
			linter.InspectNode, // register fresh linter hook
		))
		err := dec.Decode(&cfg)
		if err != nil {
			// we reached the end of the document
			if cerrors.Is(err, io.EOF) {
				break
			}
			// check if it's a type error (document was partially decoded)
			var typeErr *yaml.TypeError
			if cerrors.As(err, &typeErr) {
				err = p.handleYamlTypeError(ctx, typeErr)
			}
			// check if we recovered from the error
			if err != nil {
				return nil, cerrors.Errorf("parsing error: %w", err)
			}
		}
		linter.Warnings().Log(ctx, p.logger)
		configs = append(configs, cfg)
	}

	return configs, nil
}

func (p *Parser) handleYamlTypeError(ctx context.Context, typeErr *yaml.TypeError) error {
	for _, uerr := range typeErr.Errors {
		if _, ok := uerr.(*yaml.UnknownFieldError); !ok {
			// we don't tolerate any other error except unknown field
			return typeErr
		}
	}
	// only UnknownFieldErrors found, log them
	for _, uerr := range typeErr.Errors {
		e := p.logger.Warn(ctx).
			Int("line", uerr.Line()).
			Int("column", uerr.Column())
		e.Msg(uerr.Error())
	}
	return nil
}
