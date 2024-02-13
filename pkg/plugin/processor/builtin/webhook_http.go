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
	"net/http"
	"reflect"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/jpillora/backoff"
	"github.com/mitchellh/mapstructure"
)

var defaultWebhookHTTPConfig = webhootHTTPConfig{
	Method:             http.MethodPost,
	ContentType:        "application/json",
	BackoffRetryCount:  0,
	BackoffRetryMin:    100 * time.Millisecond,
	BackoffRetryMax:    5 * time.Second,
	BackoffRetryFactor: 2,
}

// HTTPRequest builds a processor that sends an HTTP request to the specified URL with the specified HTTP method
// (default is POST) with a content-type header as the specified value (default is application/json). the whole
// record as json will be used as the request body and the raw response body will be set under Record.Payload.After.
// if the response code is (204 No Content) then the record will be filtered out.

type webhootHTTPConfig struct {
	URL         string `mapstructure:"url"`
	Method      string `mapstructure:"method"`
	ContentType string `mapstructure:"contentType"`

	BackoffRetryCount  float64       `mapstructure:"backoffRetry.count"`
	BackoffRetryMin    time.Duration `mapstructure:"backoffRetry.min"`
	BackoffRetryMax    time.Duration `mapstructure:"backoffRetry.max"`
	BackoffRetryFactor float64       `mapstructure:"backoffRetry.factor"`
}

type webhookHTTP struct {
	sdk.UnimplementedProcessor

	config webhootHTTPConfig
}

func NewWebhookHTTP() sdk.Processor {
	return &webhookHTTP{}
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

func (w *webhookHTTP) Configure(_ context.Context, cfgMap map[string]string) error {
	cfg := defaultWebhookHTTPConfig
	err := w.decodeConfig(&cfg, cfgMap)
	if err != nil {
		return cerrors.Errorf("failed parsing configuration: %w", err)
	}
	if cfg.URL == "" {
		return cerrors.Errorf("missing required parameter 'url'")
	}

	// preflight check
	_, err = http.NewRequest(cfg.Method, cfg.URL, bytes.NewReader([]byte{}))
	if err != nil {
		return cerrors.Errorf("configuration check failed: %w", err)
	}
	w.config = cfg
	return nil
}

func (w *webhookHTTP) Open(ctx context.Context) error {
	return nil
}

func (w *webhookHTTP) Process(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, len(records))
	for i, rec := range records {
		out[i] = w.processRecordWithBackOff(ctx, rec)
	}

	return out
}

func (w *webhookHTTP) Teardown(ctx context.Context) error {
	return nil
}

func (w *webhookHTTP) processRecordWithBackOff(ctx context.Context, r opencdc.Record) sdk.ProcessedRecord {
	// default retry values
	b := &backoff.Backoff{
		Factor: w.config.BackoffRetryFactor,
		Min:    w.config.BackoffRetryMin,
		Max:    w.config.BackoffRetryMax,
	}

	for {
		processed := w.processRecord(ctx, r)
		_, isErr := processed.(sdk.ErrorRecord)
		if isErr && b.Attempt() < w.config.BackoffRetryCount {
			// TODO log message that we are retrying, include error cause (we don't have access to a proper logger)
			time.Sleep(b.Duration())
			continue
		}
		b.Reset() // reset for next processor execution
		return processed
	}

}

func (w *webhookHTTP) processRecord(ctx context.Context, r opencdc.Record) sdk.ProcessedRecord {
	jsonRec := r.Bytes()

	req, err := http.NewRequestWithContext(
		ctx,
		w.config.Method,
		w.config.URL,
		bytes.NewReader(jsonRec),
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

	if resp.StatusCode > 299 {
		// regard status codes over 299 as errors
		return sdk.ErrorRecord{Error: cerrors.Errorf("invalid status code %v (body: %q)", resp.StatusCode, string(body))}
	}
	// skip if body has no content
	if resp.StatusCode == http.StatusNoContent {
		return sdk.FilterRecord{}
	}

	r.Payload.After = opencdc.RawData(body)
	return sdk.SingleRecord(r)
}

func (w *webhookHTTP) decodeConfig(cfg *webhootHTTPConfig, cfgMap map[string]string) error {
	decCfg := &mapstructure.DecoderConfig{
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			ToDurationDecoderHook(),
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

func ToDurationDecoderHook() mapstructure.DecodeHookFunc {
	return func(f reflect.Type, t reflect.Type, data interface{}) (interface{}, error) {
		if t != reflect.TypeOf(time.Duration(0)) {
			return data, nil
		}

		if f.Kind() == reflect.String {
			return time.ParseDuration(data.(string))
		}

		return data, nil
	}
}
