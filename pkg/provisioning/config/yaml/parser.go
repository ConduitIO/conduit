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
	"fmt"
	"io"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/conduitio/conduit/pkg/provisioning/config/yaml/internal"
	v1 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v1"
	v2 "github.com/conduitio/conduit/pkg/provisioning/config/yaml/v2"
	"github.com/conduitio/yaml/v3"
)

// versionField is the name of the top-level pipeline-config version field.
const versionField = "version"

const LatestVersion = v2.LatestVersion

// changelogs contains the changelogs from all versions
var changelogs = []internal.Changelog{v1.Changelog, v2.Changelog}

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
	// Return whatever parsed successfully alongside any per-document errors, so a
	// single bad document doesn't discard the valid pipelines in the same file
	// (see #2255). Callers that require strictness can check the error.
	return configs.ToConfig(), err
}

// Warning is one advisory lint finding surfaced by ParseWithWarnings — a
// deprecated/renamed/unknown field or a version fallback, located by line and
// column. It is the exported view of the parser's internal warning type so the
// `pipelines lint` engine can render the warnings that the run/provisioning
// path only logs.
type Warning struct {
	Field   string
	Line    int
	Column  int
	Value   string
	Message string
}

// ParseWithWarnings is Parse plus the advisory warnings the parser would
// otherwise only log. It is additive and offline: the run/provisioning path
// keeps using Parse/ParseConfigurations (which continue to log warnings), so
// exposing warnings here changes no run behavior — it only gives `pipelines
// lint` a return channel for the warnings it renders.
func (p *Parser) ParseWithWarnings(ctx context.Context, reader io.Reader) ([]config.Pipeline, []Warning, error) {
	configs, warn, err := p.parse(ctx, reader)
	warn = warn.Sort()
	ws := make([]Warning, len(warn))
	for i, w := range warn {
		ws[i] = Warning{Field: w.field, Line: w.line, Column: w.column, Value: w.value, Message: w.message}
	}
	return configs.ToConfig(), ws, err
}

// ParseConfigurations parses every pipeline configuration document in reader
// and logs any advisory warnings — the historical behavior the run/provisioning
// path relies on. lint uses ParseWithWarnings instead to render them.
func (p *Parser) ParseConfigurations(ctx context.Context, reader io.Reader) (Configurations, error) {
	configs, warn, err := p.parse(ctx, reader)
	// sort warnings and log them (unchanged run behavior)
	warn.Sort().Log(ctx, p.logger)
	return configs, err
}

// parse is the shared parse loop for ParseConfigurations (which logs the
// returned warnings) and ParseWithWarnings (which returns them to the caller).
// It never logs, so the caller decides the warnings' fate.
func (p *Parser) parse(ctx context.Context, reader io.Reader) (Configurations, warnings, error) {
	// we redirect everything read from reader to buffer with TeeReader, so that
	// we can first parse the version of the file and choose what type we
	// actually need to parse the configuration
	var buffer bytes.Buffer
	reader = io.TeeReader(reader, &buffer)
	versionDecoder := yaml.NewDecoder(reader)
	configurationDecoder := yaml.NewDecoder(&buffer)

	var configs Configurations
	var warn warnings
	// errs collects per-document failures. A single bad document (e.g. an
	// unrecognized version) must not discard the valid documents around it in a
	// multi-document file, so we keep parsing and aggregate the errors. See #2255.
	var errs []error
	for {
		version, w, err := p.parseVersion(versionDecoder)
		warn = append(warn, w...)
		if err != nil {
			// we probably reached the end of the file
			if cerrors.Is(err, io.EOF) {
				break
			}
			// The version of this document could not be determined. The version
			// decoder already consumed the document, so advance the configuration
			// decoder past it too, keeping the two decoders in sync, then continue
			// with the next document.
			errs = append(errs, cerrors.Errorf("parsing error: %w", err))
			var skip any
			if skipErr := configurationDecoder.Decode(&skip); skipErr != nil {
				if cerrors.Is(skipErr, io.EOF) {
					break
				}
				// the decoders can no longer be kept in sync, stop parsing the
				// rest of the file rather than risk misattributing documents
				p.logger.Warn(ctx).Err(skipErr).Msg("could not recover parser after an invalid document; any remaining documents in this file were not processed")
				errs = append(errs, cerrors.Errorf("could not skip invalid document: %w", skipErr))
				break
			}
			continue
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
				// The document had a recognized version (so it was valid YAML and
				// the configuration decoder consumed it), but decoding into the
				// versioned config failed. Isolate the failure to this document
				// and continue with the next one.
				errs = append(errs, cerrors.Errorf("decoding error: %w", err))
				continue
			}
		}

		configs = append(configs, cfg)
	}

	return configs, warn, cerrors.Join(errs...)
}

// parseVersion will return the version that should be used to parse the
// configuration and any warnings if we defaulted to a version that's compatible
// with the requested one. If we could not recognize the version the function
// returns an error.
func (p *Parser) parseVersion(dec *yaml.Decoder) (string, warnings, error) {
	var out struct {
		Version string `yaml:"version"`
	}

	// versionNode will store the node that contains the version field (for warning)
	var versionNode yaml.Node
	dec.WithHook(func(path []string, node *yaml.Node) {
		if len(path) == 1 && path[0] == versionField {
			versionNode = *node
		}
	})

	err := dec.Decode(&out)
	if err != nil {
		return "", nil, err
	}

	// if version is empty we default to the latest version
	if out.Version == "" {
		return LatestVersion, warnings{warning{
			field:   versionField,
			line:    versionNode.Line,
			column:  versionNode.Column,
			value:   versionNode.Value,
			message: fmt.Sprintf("no version defined, falling back to parser version %v", LatestVersion),
		}}, nil
	}

	// if we recognize the version (i.e. it's in our changelog) we use it
	for _, cl := range changelogs {
		if _, ok := cl[out.Version]; ok {
			return out.Version, nil, nil // it's a recognized version
		}
	}

	// we did not recognize the version, we check if we even know the major version
	switch strings.Split(out.Version, ".")[0] {
	case v1.MajorVersion:
		return v1.LatestVersion, warnings{warning{
			field:   versionField,
			line:    versionNode.Line,
			column:  versionNode.Column,
			value:   versionNode.Value,
			message: fmt.Sprintf("unrecognized version %v, falling back to parser version %v", out.Version, v1.LatestVersion),
		}}, nil
	case v2.MajorVersion:
		return v2.LatestVersion, warnings{warning{
			field:   versionField,
			line:    versionNode.Line,
			column:  versionNode.Column,
			value:   versionNode.Value,
			message: fmt.Sprintf("unrecognized version %v, falling back to parser version %v", out.Version, v2.LatestVersion),
		}}, nil
	}

	// unrecognized version, we can't parse the configuration
	return "", nil, cerrors.Errorf("unrecognized version %v", out.Version)
}

func (p *Parser) parseConfiguration(dec *yaml.Decoder, version string) (Configuration, warnings, error) {
	// set up decoder hooks
	var w warnings
	dec.KnownFields(true)
	dec.WithHook(multiDecoderHook(
		envDecoderHook,                    // replace environment variables with their values
		p.linter.DecoderHook(version, &w), // lint config as it's parsed
	))

	switch strings.Split(version, ".")[0] {
	case v1.MajorVersion:
		var cfg v1.Configuration
		return cfg, w, dec.Decode(&cfg)
	case v2.MajorVersion:
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
