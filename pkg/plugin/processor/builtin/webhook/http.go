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

//go:generate paramgen -output=http_paramgen.go httpConfig

package webhook

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/jpillora/backoff"
	"github.com/mitchellh/mapstructure"
)

type httpConfig struct {
	// URL used in the HTTP request.
	URL string `json:"request.url" validate:"required"`
	// HTTP request method to be used.
	Method string `json:"request.method" default:"POST"`
	// Value of the Content-Type header.
	ContentType string `json:"request.contentType" default:"application/json"`

	// Maximum number of retries for an individual record when backing off following an error.
	BackoffRetryCount float64 `json:"backoffRetry.count" default:"0"`
	// The multiplying factor for each increment step.
	BackoffRetryFactor float64 `json:"backoffRetry.factor" default:"2"`
	// Minimum waiting time before retrying.
	BackoffRetryMin time.Duration `json:"backoffRetry.min" default:"100ms"`
	// Maximum waiting time before retrying.
	BackoffRetryMax time.Duration `json:"backoffRetry.max" default:"5s"`

	// RequestBodyRef specifies which field from the input record
	// should be used as the body in the HTTP request.
	// The value of this parameter should be a valid record field reference:
	// See: sdk.NewReferenceResolver
	RequestBodyRef string `json:"request.body" default:"."`
	// ResponseBodyRef specifies to which field should the
	// response body be saved to.
	// The value of this parameter should be a valid record field reference:
	// See: sdk.NewReferenceResolver
	ResponseBodyRef string `json:"response.body" default:".Payload.After"`
	// ResponseStatusRef specifies to which field should the
	// response status be saved to.
	// The value of this parameter should be a valid record field reference:
	// See: sdk.NewReferenceResolver
	ResponseStatusRef string `json:"response.status"`
}

type httpProcessor struct {
	sdk.UnimplementedProcessor

	logger log.CtxLogger
	config httpConfig

	requestBodyRef    *sdk.ReferenceResolver
	responseBodyRef   *sdk.ReferenceResolver
	responseStatusRef *sdk.ReferenceResolver
}

func NewWebhookHTTP(l log.CtxLogger) sdk.Processor {
	return &httpProcessor{logger: l.WithComponent("builtin.httpProcessor")}
}

func (p *httpProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "webhook.http",
		Summary: "HTTP webhook processor",
		Description: `
A processor that sends an HTTP request to the specified URL with the specified HTTP method
(default is POST) with a content-type header as the specified value (default is application/json).

The whole record as json will be used as the request body and the raw response body will be set under 
Record.Payload.After. If the response code is (204 No Content) then the record will be filtered out.
`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: httpConfig{}.Parameters(),
	}, nil
}

func (p *httpProcessor) Configure(_ context.Context, m map[string]string) error {
	cfg := httpConfig{}
	inputCfg := config.Config(m).
		Sanitize().
		ApplyDefaults(cfg.Parameters())

	err := inputCfg.Validate(cfg.Parameters())
	if err != nil {
		return cerrors.Errorf("invalid configuration: %w", err)
	}
	if inputCfg["response.body"] == inputCfg["response.status"] {
		return cerrors.New("invalid configuration: response.body and response.status set to same field")
	}

	err = inputCfg.DecodeInto(&cfg)
	if err != nil {
		return cerrors.Errorf("failed decoding configuration: %w", err)
	}

	requestBodyRef, err := sdk.NewReferenceResolver(cfg.RequestBodyRef)
	if err != nil {
		return cerrors.Errorf("failed parsing request.body %v: %w", p.config.RequestBodyRef, err)
	}
	p.requestBodyRef = &requestBodyRef

	responseBodyRef, err := sdk.NewReferenceResolver(cfg.ResponseBodyRef)
	if err != nil {
		return cerrors.Errorf("failed parsing response.body %v: %w", p.config.ResponseBodyRef, err)
	}
	p.responseBodyRef = &responseBodyRef

	if cfg.ResponseStatusRef != "" {
		responseStatusRef, err := sdk.NewReferenceResolver(cfg.ResponseStatusRef)
		if err != nil {
			return cerrors.Errorf("failed parsing response.status %v: %w", p.config.ResponseStatusRef, err)
		}
		p.responseStatusRef = &responseStatusRef
	}

	// preflight check
	_, err = http.NewRequest(cfg.Method, cfg.URL, bytes.NewReader([]byte{}))
	if err != nil {
		return cerrors.Errorf("configuration check failed: %w", err)
	}
	p.config = cfg

	return nil
}

func (p *httpProcessor) Open(context.Context) error {
	return nil
}

func (p *httpProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i, rec := range records {
		out[i] = p.processRecordWithBackOff(ctx, rec)
	}

	return out
}

func (p *httpProcessor) processRecordWithBackOff(ctx context.Context, r opencdc.Record) sdk.ProcessedRecord {
	b := &backoff.Backoff{
		Factor: p.config.BackoffRetryFactor,
		Min:    p.config.BackoffRetryMin,
		Max:    p.config.BackoffRetryMax,
	}

	for {
		processed := p.processRecord(ctx, r)
		errRec, isErr := processed.(sdk.ErrorRecord)
		attempt := b.Attempt()
		duration := b.Duration()

		if isErr && attempt < p.config.BackoffRetryCount {
			p.logger.Debug(ctx).
				Err(errRec.Error).
				Float64("attempt", attempt).
				Float64("backoffRetry.count", p.config.BackoffRetryCount).
				Int64("backoffRetry.duration", duration.Milliseconds()).
				Msg("retrying HTTP request")

			time.Sleep(duration)
			continue
		}
		b.Reset() // reset for next processor execution
		return processed
	}
}

// processRecord processes a single record (without retries)
func (p *httpProcessor) processRecord(ctx context.Context, r opencdc.Record) sdk.ProcessedRecord {
	var key []byte
	if r.Key != nil {
		key = r.Key.Bytes()
	}
	p.logger.Trace(ctx).Bytes("record_key", key).Msg("processing record")

	req, err := p.buildRequest(ctx, r)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("cannot create HTTP request: %w", err)}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("error executing HTTP request: %w", err)}
	}
	defer func() {
		errClose := resp.Body.Close()
		if errClose != nil {
			p.logger.Debug(ctx).
				Err(errClose).
				Msg("failed closing response body (possible resource leak)")
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("error reading response body: %w", err)}
	}

	if resp.StatusCode >= 300 {
		// regard status codes over 299 as errors
		return sdk.ErrorRecord{Error: cerrors.Errorf("error status code %v (body: %q)", resp.StatusCode, string(body))}
	}
	// skip if body has no content
	if resp.StatusCode == http.StatusNoContent {
		return sdk.FilterRecord{}
	}

	// Set response body
	err = p.setField(&r, p.responseBodyRef, opencdc.RawData(body))
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed setting response body: %w", err)}
	}
	err = p.setField(&r, p.responseStatusRef, strconv.Itoa(resp.StatusCode))
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed setting response status: %w", err)}
	}

	return sdk.SingleRecord(r)
}

func (p *httpProcessor) buildRequest(ctx context.Context, r opencdc.Record) (*http.Request, error) {
	reqBody, err := p.requestBody(r)
	if err != nil {
		return nil, cerrors.Errorf("failed getting request body: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		p.config.Method,
		p.config.URL,
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return nil, cerrors.Errorf("error creating HTTP request: %w", err)
	}

	// todo make it possible to add more headers, e.g. auth headers etc.
	req.Header.Set("Content-Type", p.config.ContentType)

	return req, nil
}

// requestBody returns the request body for the given record,
// using the configured field reference (see: request.body configuration parameter).
func (p *httpProcessor) requestBody(r opencdc.Record) ([]byte, error) {
	ref, err := p.requestBodyRef.Resolve(&r)
	if err != nil {
		return nil, cerrors.Errorf("failed resolving request.body: %w", err)
	}

	val := ref.Get()
	// Raw byte data should be sent as it is, as that's most often what we want
	// If we json.Marshal it first, it will be Base64-encoded.
	if raw, ok := val.(opencdc.RawData); ok {
		return raw.Bytes(), nil
	}

	return json.Marshal(val)
}

func (p *httpProcessor) setField(r *opencdc.Record, refRes *sdk.ReferenceResolver, data any) error {
	if refRes == nil {
		return nil
	}

	ref, err := refRes.Resolve(r)
	if err != nil {
		return err
	}

	err = ref.Set(data)
	if err != nil {
		return err
	}

	return nil
}

// ToDurationDecoderHook returns a mapstructure.DecodeHookFunc
// that decodes a string into a time.Duration.
func ToDurationDecoderHook() mapstructure.DecodeHookFunc {
	return func(from reflect.Type, to reflect.Type, data interface{}) (interface{}, error) {
		if to != reflect.TypeOf(time.Duration(0)) {
			return data, nil
		}

		if from.Kind() == reflect.String {
			return time.ParseDuration(data.(string))
		}

		return data, nil
	}
}

// ToReferenceResolvedDecodeHook returns a mapstructure.DecodeHookFunc
// that decodes a string into a sdk.ReferenceResolver.
func ToReferenceResolvedDecodeHook() mapstructure.DecodeHookFunc {
	return func(from reflect.Type, to reflect.Type, data interface{}) (interface{}, error) {
		if to != reflect.TypeOf(sdk.ReferenceResolver{}) {
			return data, nil
		}

		if from.Kind() == reflect.String {
			return sdk.NewReferenceResolver(data.(string))
		}

		return data, nil
	}
}

func (p *httpProcessor) Teardown(context.Context) error {
	return nil
}
