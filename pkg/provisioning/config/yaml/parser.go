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
	"bytes"
	"context"
	"io"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	v1 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v1"
	v2 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v2"
	"github.com/conduitio/yaml/v3"
)

const VersionLatest = v2.VersionLatest

type Parser struct {
	linter *configLinter
	logger log.CtxLogger
}

// Configuration is parsed by a yaml Parser.
type Configuration interface {
	ToConfig() []config.Pipeline
}

type Configurations []Configuration

func (c Configurations) ToConfig() []config.Pipeline {
	if len(c) == 0 {
		return nil
	}

	out := make([]config.Pipeline, 0, len(c))
	for _, cfg := range c {
		out = append(out, cfg.ToConfig()...)
	}
	return out
}

func NewParser(logger log.CtxLogger) *Parser {
	return &Parser{
		linter: newConfigLinter(v1.Changelog, v2.Changelog),
		logger: logger.WithComponent("yaml.Parser"),
	}
}

func (p *Parser) Parse(ctx context.Context, reader io.Reader) ([]config.Pipeline, error) {
	configs, err := p.ParseConfigurations(ctx, reader)
	if err != nil {
		return nil, err
	}

	return configs.ToConfig(), nil
}

func (p *Parser) ParseConfigurations(ctx context.Context, reader io.Reader) (Configurations, error) {
	// we redirect everything read from reader to buffer with TeeReader, so that
	// we can first parse the version of the file and choose what type we
	// actually need to parse the configuration
	var buffer bytes.Buffer
	reader = io.TeeReader(reader, &buffer)
	versionDecoder := yaml.NewDecoder(reader)
	configurationDecoder := yaml.NewDecoder(&buffer)

	var configs Configurations
	var warn warnings
	for {
		version, err := p.parseVersion(versionDecoder)
		if err != nil {
			// we probably reached the end of the document
			if cerrors.Is(err, io.EOF) {
				break
			}
			return nil, cerrors.Errorf("parsing error: %w", err)
		}

		cfg, w, err := p.parseConfiguration(configurationDecoder, version)
		warn = append(warn, w...)
		if err != nil {
			// check if it's a type error (document was partially decoded)
			var typeErr *yaml.TypeError
			if cerrors.As(err, &typeErr) {
				w, err = p.yamlTypeErrorToWarnings(typeErr)
				warn = append(warn, w...)
			}
			// check if we recovered from the error
			if err != nil {
				return nil, cerrors.Errorf("decoding error: %w", err)
			}
		}

		configs = append(configs, cfg)
	}

	// sort warnings and log them
	warn.Sort().Log(ctx, p.logger)
	return configs, nil
}

func (p *Parser) parseVersion(dec *yaml.Decoder) (string, error) {
	var out struct {
		Version string `yaml:"version"`
	}
	err := dec.Decode(&out)
	if err != nil {
		return "", err
	}
	return out.Version, nil
}

func (p *Parser) parseConfiguration(dec *yaml.Decoder, version string) (Configuration, warnings, error) {
	if version == "" {
		// if no version is set, default to latest
		version = VersionLatest
	}

	// set up decoder hooks
	var w warnings
	dec.KnownFields(true)
	dec.WithHook(multiDecoderHook(
		envDecoderHook,                    // replace environment variables with their values
		p.linter.DecoderHook(version, &w), // lint config as it's parsed
	))

	switch {
	case v1.VersionRegex.MatchString(version):
		var cfg v1.Configuration
		return cfg, w, dec.Decode(&cfg)
	case v2.VersionRegex.MatchString(version):
		var cfg v2.Configuration
		return cfg, w, dec.Decode(&cfg)
	default:
		return nil, nil, cerrors.Errorf("unrecognized config version %v", version)
	}
}

// yamlTypeErrorToWarnings converts yaml.TypeError to warnings if it only
// contains recoverable errors. If it contains at least one actual error it
// returns nil and the error itself.
func (p *Parser) yamlTypeErrorToWarnings(typeErr *yaml.TypeError) (warnings, error) {
	warn := make(warnings, len(typeErr.Errors))
	for i, uerr := range typeErr.Errors {
		switch uerr := uerr.(type) {
		case *yaml.UnknownFieldError:
			warn[i] = warning{
				field:   uerr.Field(),
				line:    uerr.Line(),
				column:  uerr.Column(),
				value:   "", // no value in UnknownFieldError
				message: uerr.Error(),
			}
		default:
			// we don't tolerate any other errors
			return nil, typeErr
		}
	}
	return warn, nil
}
