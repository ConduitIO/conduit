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

package builtin

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"reflect"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/jpillora/backoff"
	"github.com/mitchellh/mapstructure"
)

var defaultWebhookHTTPConfig = map[string]string{
	"request.method":      http.MethodPost,
	"request.contentType": "application/json",

	"backoffRetry.count":  "0",
	"backoffRetry.min":    "100ms",
	"backoffRetry.max":    "5s",
	"backoffRetry.factor": "2",

	"request.body":  ".",
	"response.body": ".Payload.After",
}

type webhookHTTPConfig struct {
	URL         string `mapstructure:"request.url"`
	Method      string `mapstructure:"request.method"`
	ContentType string `mapstructure:"request.contentType"`

	BackoffRetryCount  float64       `mapstructure:"backoffRetry.count"`
	BackoffRetryMin    time.Duration `mapstructure:"backoffRetry.min"`
	BackoffRetryMax    time.Duration `mapstructure:"backoffRetry.max"`
	BackoffRetryFactor float64       `mapstructure:"backoffRetry.factor"`

	// RequestBodyRef specifies which field from the input record
	// should be used as the body in the HTTP request.
	// The value of this parameter should be a valid record field reference:
	// See: sdk.NewReferenceResolver
	RequestBodyRef *sdk.ReferenceResolver `mapstructure:"request.body"`
	// ResponseBodyRef specifies to which field should the
	// response body be saved to.
	// The value of this parameter should be a valid record field reference:
	// See: sdk.NewReferenceResolver
	ResponseBodyRef *sdk.ReferenceResolver `mapstructure:"response.body"`
	// ResponseStatusRef specifies to which field should the
	// response status be saved to.
	// The value of this parameter should be a valid record field reference:
	// See: sdk.NewReferenceResolver
	ResponseStatusRef *sdk.ReferenceResolver `mapstructure:"response.status"`
}

type webhookHTTP struct {
	sdk.UnimplementedProcessor

	logger log.CtxLogger

	config webhookHTTPConfig
}

func NewWebhookHTTP(l log.CtxLogger) sdk.Processor {
	return &webhookHTTP{logger: l.WithComponent("builtin.webhookHTTP")}
}

func (w *webhookHTTP) Specification() (sdk.Specification, error) {
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
		Parameters: nil,
	}, nil
}

func (w *webhookHTTP) Configure(_ context.Context, userCfgMap map[string]string) error {
	cfgMap := w.withDefaultConfig(userCfgMap)
	// Check required parameters
	if cfgMap["request.url"] == "" {
		return cerrors.Errorf("missing required parameter 'url'")
	}
	if cfgMap["response.body"] == cfgMap["response.status"] {
		return cerrors.Errorf("response.body and response.status set to same field")
	}

	cfg := webhookHTTPConfig{}
	err := w.decodeConfig(&cfg, cfgMap)
	if err != nil {
		return cerrors.Errorf("failed parsing configuration: %w", err)
	}

	// preflight check
	_, err = http.NewRequest(cfg.Method, cfg.URL, bytes.NewReader([]byte{}))
	if err != nil {
		return cerrors.Errorf("configuration check failed: %w", err)
	}
	w.config = cfg

	return nil
}

func (w *webhookHTTP) Open(context.Context) error {
	return nil
}

func (w *webhookHTTP) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i, rec := range records {
		out[i] = w.processRecordWithBackOff(ctx, rec)
	}

	return out
}

func (w *webhookHTTP) Teardown(context.Context) error {
	return nil
}

func (w *webhookHTTP) processRecordWithBackOff(ctx context.Context, r opencdc.Record) sdk.ProcessedRecord {
	b := &backoff.Backoff{
		Factor: w.config.BackoffRetryFactor,
		Min:    w.config.BackoffRetryMin,
		Max:    w.config.BackoffRetryMax,
	}

	for {
		processed := w.processRecord(ctx, r)
		errRec, isErr := processed.(sdk.ErrorRecord)
		attempt := b.Attempt()
		duration := b.Duration()

		if isErr && attempt < w.config.BackoffRetryCount {
			w.logger.Debug(ctx).
				Err(errRec.Error).
				Float64("attempt", attempt).
				Float64("backoffRetry.count", w.config.BackoffRetryCount).
				Int64("backoffRetry.duration", duration.Milliseconds()).
				Msg("retrying HTTP request")

			time.Sleep(duration)
			continue
		}
		b.Reset() // reset for next processor execution
		return processed
	}
}

func (w *webhookHTTP) processRecord(ctx context.Context, r opencdc.Record) sdk.ProcessedRecord {
	reqBody, err := w.getRequestBody(r)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed getting request body: %w")}
	}

	req, err := http.NewRequestWithContext(
		ctx,
		w.config.Method,
		w.config.URL,
		bytes.NewReader(reqBody),
	)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("error trying to create HTTP request: %w", err)}
	}

	req.Header.Set("Content-Type", w.config.ContentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("error trying to execute HTTP request: %w", err)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("error trying to read response body: %w", err)}
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
	err = w.setField(&r, w.config.ResponseBodyRef, opencdc.RawData(body))
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed setting response body: %w", err)}
	}
	err = w.setField(&r, w.config.ResponseStatusRef, resp.StatusCode)
	if err != nil {
		return sdk.ErrorRecord{Error: cerrors.Errorf("failed setting response status: %w", err)}
	}

	return sdk.SingleRecord(r)
}

func (w *webhookHTTP) decodeConfig(cfg *webhookHTTPConfig, cfgMap map[string]string) error {
	decCfg := &mapstructure.DecoderConfig{
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			ToDurationDecoderHook(),
			ToReferenceResolvedDecodeHook(),
		),
		Result: &cfg,
	}
	dec, err := mapstructure.NewDecoder(decCfg)
	if err != nil {
		return cerrors.Errorf("failed creating new decoder: %w", err)
	}

	err = dec.Decode(cfgMap)
	if err != nil {
		return cerrors.Errorf("failed decoding map: %w", err)
	}

	return nil
}

func (w *webhookHTTP) withDefaultConfig(userCfgMap map[string]string) map[string]string {
	out := maps.Clone(defaultWebhookHTTPConfig)
	maps.Copy(out, userCfgMap)

	return out
}

func (w *webhookHTTP) setField(r *opencdc.Record, refRes *sdk.ReferenceResolver, data any) error {
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

func (w *webhookHTTP) getRequestBody(r opencdc.Record) ([]byte, error) {
	ref, err := w.config.RequestBodyRef.Resolve(&r)
	if err != nil {
		return nil, cerrors.Errorf("failed resolving request.body: %w", err)
	}

	val := ref.Get()
	if raw, ok := val.(opencdc.RawData); ok {
		return raw.Bytes(), nil
	}

	return json.Marshal(val)
}

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
