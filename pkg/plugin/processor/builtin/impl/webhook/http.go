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
	"net/url"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/jpillora/backoff"
)

type httpConfig struct {
	// URL is a Go template expression for the URL used in the HTTP request, using Go [templates](https://pkg.go.dev/text/template).
	// The value provided to the template is [opencdc.Record](https://pkg.go.dev/github.com/conduitio/conduit-commons/opencdc#Record),
	// so the template has access to all its fields (e.g. `.Position`, `.Key`, `.Metadata`, and so on). We also inject all template functions provided by [sprig](https://masterminds.github.io/sprig/)
	// to make it easier to write templates.
	URL string `json:"request.url" validate:"required"`
	// Method is the HTTP request method to be used.
	Method string `json:"request.method" default:"GET"`
	// Deprecated: use `headers.Content-Type` instead.
	ContentType string `json:"request.contentType"`
	// Headers to add to the request, use `headers.*` to specify the header and its value (e.g. `headers.Authorization: "Bearer key"`).
	Headers map[string]string `json:"headers"`

	// Maximum number of retries for an individual record when backing off following an error.
	BackoffRetryCount float64 `json:"backoffRetry.count" default:"0" validate:"gt=-1"`
	// The multiplying factor for each increment step.
	BackoffRetryFactor float64 `json:"backoffRetry.factor" default:"2" validate:"gt=0"`
	// The minimum waiting time before retrying.
	BackoffRetryMin time.Duration `json:"backoffRetry.min" default:"100ms"`
	// The maximum waiting time before retrying.
	BackoffRetryMax time.Duration `json:"backoffRetry.max" default:"5s"`

	// Specifies the body that will be sent in the HTTP request. The field accepts
	// a Go [templates](https://pkg.go.dev/text/template) that's evaluated using the
	// [opencdc.Record](https://pkg.go.dev/github.com/conduitio/conduit-commons/opencdc#Record)
	// as input. By default, the body is empty.
	//
	// To send the whole record as JSON you can use `{{ toJson . }}`.
	RequestBodyTmpl string `json:"request.body"`
	// Specifies in which field should the response body be saved.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).
	ResponseBodyRef string `json:"response.body" default:".Payload.After"`
	// Specifies in which field should the response status be saved. If no value
	// is set, then the response status will NOT be saved.
	//
	// For more information about the format, see [Referencing fields](https://conduit.io/docs/processors/referencing-fields).
	ResponseStatusRef string `json:"response.status"`
}

func (c *httpConfig) parseHeaders() error {
	if c.Headers == nil {
		c.Headers = make(map[string]string)
	}

	var isContentTypeSet bool
	for name := range c.Headers {
		if strings.ToLower(name) == "content-type" {
			isContentTypeSet = true
			break
		}
	}

	switch {
	case isContentTypeSet && c.ContentType != "":
		return cerrors.Errorf(`configuration error, cannot provide both "request.contentType" and "headers.Content-Type", use "headers.Content-Type" only`)
	case !isContentTypeSet && c.ContentType != "":
		// Use contents of deprecated field.
		c.Headers["Content-Type"] = c.ContentType
		c.ContentType = ""
	case !isContentTypeSet:
		// By default, we set the Content-Type to application/json.
		c.Headers["Content-Type"] = "application/json"
	}

	return nil
}

type httpProcessor struct {
	sdk.UnimplementedProcessor

	logger log.CtxLogger

	config     httpConfig
	backoffCfg *backoff.Backoff

	urlTmpl         *template.Template
	requestBodyTmpl *template.Template

	responseBodyRef   *sdk.ReferenceResolver
	responseStatusRef *sdk.ReferenceResolver
}

func NewHTTPProcessor(l log.CtxLogger) sdk.Processor {
	return &httpProcessor{logger: l.WithComponent("webhook.httpProcessor")}
}

func (p *httpProcessor) Specification() (sdk.Specification, error) {
	return sdk.Specification{
		Name:    "webhook.http",
		Summary: "Trigger an HTTP request for every record.",
		Description: `A processor that sends an HTTP request to the specified URL, retries on error and 
saves the response body and, optionally, the response status.

A status code over 500 is regarded as an error and will cause the processor to retry the request.
The processor will retry the request according to the backoff configuration.`,
		Version:    "v0.1.0",
		Author:     "Meroxa, Inc.",
		Parameters: httpConfig{}.Parameters(),
	}, nil
}

func (p *httpProcessor) Configure(ctx context.Context, c config.Config) error {
	err := sdk.ParseConfig(ctx, c, &p.config, p.config.Parameters())
	if err != nil {
		return cerrors.Errorf("failed parsing configuration: %w", err)
	}

	err = p.config.parseHeaders()
	if err != nil {
		return err
	}

	if p.config.ResponseBodyRef == p.config.ResponseStatusRef {
		return cerrors.New("invalid configuration: response.body and response.status set to same field")
	}

	// parse request body template
	if strings.Contains(p.config.RequestBodyTmpl, "{{") {
		// create URL template
		p.requestBodyTmpl, err = template.New("").Funcs(sprig.FuncMap()).Parse(p.config.RequestBodyTmpl)
		if err != nil {
			return cerrors.Errorf("failed parsing request.body %v: %w", err)
		}
	}

	responseBodyRef, err := sdk.NewReferenceResolver(p.config.ResponseBodyRef)
	if err != nil {
		return cerrors.Errorf("failed parsing response.body %v: %w", p.config.ResponseBodyRef, err)
	}
	p.responseBodyRef = &responseBodyRef

	// This field is optional and, if not set, response status won't be saved.
	if p.config.ResponseStatusRef != "" {
		responseStatusRef, err := sdk.NewReferenceResolver(p.config.ResponseStatusRef)
		if err != nil {
			return cerrors.Errorf("failed parsing response.status %v: %w", p.config.ResponseStatusRef, err)
		}
		p.responseStatusRef = &responseStatusRef
	}

	// parse URL template
	if strings.Contains(p.config.URL, "{{") {
		// create URL template
		p.urlTmpl, err = template.New("").Funcs(sprig.FuncMap()).Parse(p.config.URL)
		if err != nil {
			return cerrors.Errorf("error while parsing the URL template: %w", err)
		}
	}

	// preflight check
	_, err = http.NewRequest(p.config.Method, p.config.URL, bytes.NewReader([]byte{}))
	if err != nil {
		return cerrors.Errorf("configuration check failed: %w", err)
	}

	p.backoffCfg = &backoff.Backoff{
		Factor: p.config.BackoffRetryFactor,
		Min:    p.config.BackoffRetryMin,
		Max:    p.config.BackoffRetryMax,
	}
	return nil
}

func (p *httpProcessor) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, rec := range records {
		proc, err := p.processRecordWithBackOff(ctx, rec)
		if err != nil {
			return append(out, sdk.ErrorRecord{Error: err})
		}
		out = append(out, proc)
	}

	return out
}

func (p *httpProcessor) processRecordWithBackOff(ctx context.Context, r opencdc.Record) (sdk.ProcessedRecord, error) {
	for {
		processed, err := p.processRecord(ctx, r)
		attempt := p.backoffCfg.Attempt()
		duration := p.backoffCfg.Duration()

		if err != nil && attempt < p.config.BackoffRetryCount {
			p.logger.Debug(ctx).
				Err(err).
				Float64("attempt", attempt).
				Float64("backoffRetry.count", p.config.BackoffRetryCount).
				Int64("backoffRetry.duration", duration.Milliseconds()).
				Msg("retrying HTTP request")

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(duration):
				continue
			}
		}
		p.backoffCfg.Reset() // reset for next processor execution
		if err != nil {
			return nil, err
		}

		return processed, nil
	}
}

// processRecord processes a single record (without retries)
func (p *httpProcessor) processRecord(ctx context.Context, r opencdc.Record) (sdk.ProcessedRecord, error) {
	var key []byte
	if r.Key != nil {
		key = r.Key.Bytes()
	}
	p.logger.Trace(ctx).Bytes("record_key", key).Msg("processing record")

	req, err := p.buildRequest(ctx, r)
	if err != nil {
		return nil, cerrors.Errorf("cannot create HTTP request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, cerrors.Errorf("error executing HTTP request: %w", err)
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
		return nil, cerrors.Errorf("error reading response body: %w", err)
	}

	if resp.StatusCode >= 500 {
		// regard status codes over 500 as errors
		return nil, cerrors.Errorf("error status code %v (body: %q)", resp.StatusCode, string(body))
	}

	// Set response body
	err = p.setField(&r, p.responseBodyRef, body)
	if err != nil {
		return nil, cerrors.Errorf("failed setting response body: %w", err)
	}
	err = p.setField(&r, p.responseStatusRef, strconv.Itoa(resp.StatusCode))
	if err != nil {
		return nil, cerrors.Errorf("failed setting response status: %w", err)
	}

	return sdk.SingleRecord(r), nil
}

func (p *httpProcessor) buildRequest(ctx context.Context, r opencdc.Record) (*http.Request, error) {
	reqBody, err := p.requestBody(r)
	if err != nil {
		return nil, cerrors.Errorf("failed getting request body: %w", err)
	}

	url, err := p.evaluateURL(r)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		p.config.Method,
		url,
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return nil, cerrors.Errorf("error creating HTTP request: %w", err)
	}

	// set header values
	for key, val := range p.config.Headers {
		req.Header.Set(key, val)
	}

	return req, nil
}

func (p *httpProcessor) evaluateURL(rec opencdc.Record) (string, error) {
	if p.urlTmpl == nil {
		return p.config.URL, nil
	}
	var b bytes.Buffer
	err := p.urlTmpl.Execute(&b, rec)
	if err != nil {
		return "", cerrors.Errorf("error while evaluating URL template: %w", err)
	}
	u, err := url.Parse(b.String())
	if err != nil {
		return "", cerrors.Errorf("error parsing URL: %w", err)
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", cerrors.Errorf("error parsing URL query: %w", err)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// requestBody returns the request body for the given record,
// using the configured field reference (see: request.body configuration parameter).
func (p *httpProcessor) requestBody(r opencdc.Record) ([]byte, error) {
	if p.requestBodyTmpl == nil {
		if p.config.RequestBodyTmpl != "" {
			return []byte(p.config.RequestBodyTmpl), nil
		}
		return nil, nil
	}

	var b bytes.Buffer
	err := p.requestBodyTmpl.Execute(&b, r)
	if err != nil {
		return nil, cerrors.Errorf("error while evaluating request body template: %w", err)
	}

	return b.Bytes(), nil
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
