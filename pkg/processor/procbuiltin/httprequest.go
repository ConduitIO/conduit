// Copyright © 2022 Meroxa, Inc.
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

package procbuiltin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/jpillora/backoff"
)

const (
	httpRequestProcType = "httprequest"

	httpRequestConfigURL          = "url"
	httpRequestConfigMethod       = "method"
	httpRequestConfigContentType  = "contentType"
	httpRequestContentTypeDefault = "application/json"
	httpRequestBackoffRetryCount  = "backoffRetry.count"
	httpRequestBackoffRetryMin    = "backoffRetry.min"
	httpRequestBackoffRetryMax    = "backoffRetry.max"
	httpRequestBackoffRetryFactor = "backoffRetry.factor"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(httpRequestProcType, HTTPRequest)
}

// HTTPRequest builds a processor that sends an HTTP request to the specified URL with the specified HTTP method
// (default is POST) with a content-type header as the specified value (default is application/json). the whole
// record as json will be used as the request body and the raw response body will be set under Record.Payload.After.
// if the response code is (204 No Content) then the record will be filtered out.
func HTTPRequest(config processor.Config) (processor.Interface, error) {
	return httpRequest(httpRequestProcType, config)
}

func httpRequest(
	processorType string,
	config processor.Config,
) (processor.Interface, error) {
	var (
		err    error
		rawURL string
		method string
	)

	if rawURL, err = getConfigFieldString(config, httpRequestConfigURL); err != nil {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	}

	_, err = url.Parse(rawURL)
	if err != nil {
		return nil, cerrors.Errorf("%s: error trying to parse url: %w", processorType, err)
	}

	method = config.Settings[httpRequestConfigMethod]
	if method == "" {
		method = http.MethodPost
	}
	contentType := config.Settings[httpRequestConfigContentType]
	if contentType == "" {
		contentType = httpRequestContentTypeDefault
	}

	// preflight check
	_, err = http.NewRequest(
		method,
		rawURL,
		bytes.NewReader([]byte{}),
	)
	if err != nil {
		return nil, cerrors.Errorf("%s: error trying to create HTTP request: %w", processorType, err)
	}

	procFn := func(ctx context.Context, r record.Record) (record.Record, error) {
		jsonRec, err := json.Marshal(r)
		if err != nil {
			return record.Record{}, cerrors.Errorf("%s: error creating json record: %w", processorType, err)
		}

		req, err := http.NewRequestWithContext(
			ctx,
			method,
			rawURL,
			bytes.NewReader(jsonRec),
		)
		if err != nil {
			return record.Record{}, cerrors.Errorf("%s: error trying to create HTTP request: %w", processorType, err)
		}

		req.Header.Set("Content-Type", contentType)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return record.Record{}, cerrors.Errorf("%s: error trying to execute HTTP request: %w", processorType, err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return record.Record{}, cerrors.Errorf("%s: error trying to read response body: %w", processorType, err)
		}

		if resp.StatusCode > 299 {
			// regard status codes over 299 as errors
			return record.Record{}, cerrors.Errorf("%s: invalid status code %v (body: %q)", processorType, resp.StatusCode, string(body))
		}
		// skip if body has no content
		if resp.StatusCode == http.StatusNoContent {
			return record.Record{}, processor.ErrSkipRecord
		}

		r.Payload.After = record.RawData{Raw: body}
		return r, nil
	}

	return configureHTTPRequestBackoffRetry(processorType, config, procFn)
}

func configureHTTPRequestBackoffRetry(
	processorType string,
	config processor.Config,
	procFn func(context.Context, record.Record) (record.Record, error),
) (processor.Interface, error) {
	// retryCount is a float64 to match the backoff library attempt type
	var retryCount float64

	tmp, err := getConfigFieldInt64(config, httpRequestBackoffRetryCount)
	if err != nil && !cerrors.Is(err, errEmptyConfigField) {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	}
	retryCount = float64(tmp)

	if retryCount == 0 {
		// no retries configured, just use the plain processor
		return NewFuncWrapper(procFn), nil
	}

	// default retry values
	b := &backoff.Backoff{
		Factor: 2,
		Min:    time.Millisecond * 100,
		Max:    time.Second * 5,
	}

	min, err := getConfigFieldDuration(config, httpRequestBackoffRetryMin)
	if err != nil && !cerrors.Is(err, errEmptyConfigField) {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	} else if err == nil {
		b.Min = min
	}

	max, err := getConfigFieldDuration(config, httpRequestBackoffRetryMax)
	if err != nil && !cerrors.Is(err, errEmptyConfigField) {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	} else if err == nil {
		b.Max = max
	}

	factor, err := getConfigFieldFloat64(config, httpRequestBackoffRetryFactor)
	if err != nil && !cerrors.Is(err, errEmptyConfigField) {
		return nil, cerrors.Errorf("%s: %w", processorType, err)
	} else if err == nil {
		b.Factor = factor
	}

	// wrap processor in a retry loop
	return NewFuncWrapper(func(ctx context.Context, r record.Record) (record.Record, error) {
		for {
			r, err := procFn(ctx, r)
			if err != nil && b.Attempt() < retryCount {
				// TODO log message that we are retrying, include error cause (we don't have access to a proper logger)
				time.Sleep(b.Duration())
				continue
			}
			b.Reset() // reset for next processor execution
			return r, err
		}
	}), nil
}
