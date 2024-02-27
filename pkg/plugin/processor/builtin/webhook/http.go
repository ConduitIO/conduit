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
	"strconv"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/jpillora/backoff"
)

type httpConfig struct {
	// URL used in the HTTP request.
	URL string `json:"request.url" validate:"required"`
	// Method is the HTTP request method to be used.
	Method string `json:"request.method" default:"POST"`
	// ContentType is the value of the Content-Type header.
	ContentType string `json:"request.contentType" default:"application/json"`

	// BackoffRetryCount is the maximum number of retries for an individual record
	// when backing off following an error.
	BackoffRetryCount float64 `json:"backoffRetry.count" default:"0" validate:"gt=-1"`
	// BackoffRetryFactor is the multiplying factor for each increment step.
	BackoffRetryFactor float64 `json:"backoffRetry.factor" default:"2" validate:"gt=0"`
	// BackoffRetryMin is the minimum waiting time before retrying.
	BackoffRetryMin time.Duration `json:"backoffRetry.min" default:"100ms"`
	// BackoffRetryMax is the maximum waiting time before retrying.
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
	// The value of this parameter should be a valid record field reference.
	// If no value is set, then the response status will NOT be saved.
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
	return &httpProcessor{logger: l.WithComponent("webhook.httpProcessor")}
}

func (p *httpProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "webhook.http",
		Summary: "HTTP webhook processor",
		Description: `
A processor that sends an HTTP request to the specified URL, retries on error and 
saves the response body and, optionally, the response status. 
`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: httpConfig{}.Parameters(),
	}, nil
}

func (p *httpProcessor) Configure(ctx context.Context, m map[string]string) error {
	cfg := httpConfig{}
	err := sdk.ParseConfig(ctx, m, &cfg, cfg.Parameters())
	if err != nil {
		return cerrors.Errorf("failed parsing configuration: %w", err)
	}

	if cfg.ResponseBodyRef == cfg.ResponseStatusRef {
		return cerrors.New("invalid configuration: response.body and response.status set to same field")
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

	// This field is optional and, if not set, response status won't be saved.
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
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		procRec := p.processRecordWithBackOff(ctx, rec)
		out = append(out, procRec)
		if _, ok := procRec.(sdk.ErrorRecord); ok {
			return out
		}
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

func (p *httpProcessor) Teardown(context.Context) error {
	return nil
}
