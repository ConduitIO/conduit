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

package txfbuiltin

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/transform"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	httpRequestName = "httprequest"

	httpRequestConfigURL    = "url"
	httpRequestConfigMethod = "method"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(httpRequestName, transform.NewBuilder(HTTPRequest))
}

// HTTPRequest builds a transform that sends an HTTP request to the specified
// URL with the specified HTTP method (default is POST). The record payload is
// used as the request body and the raw response body is put into the record
// payload.
func HTTPRequest(config transform.Config) (transform.Transform, error) {
	return httpRequest(httpRequestName, config)
}

func httpRequest(
	transformName string,
	config transform.Config,
) (transform.Transform, error) {
	var (
		err    error
		rawURL string
		method string
	)

	if rawURL, err = getConfigField(config, httpRequestConfigURL); err != nil {
		return nil, cerrors.Errorf("%s: %w", transformName, err)
	}

	_, err = url.Parse(rawURL)
	if err != nil {
		return nil, cerrors.Errorf("%s: error trying to parse url: %w", transformName, err)
	}

	method = config[httpRequestConfigMethod]
	if method == "" {
		method = http.MethodPost
	}

	// preflight check
	_, err = http.NewRequest(
		method,
		rawURL,
		bytes.NewReader([]byte{}),
	)
	if err != nil {
		return nil, cerrors.Errorf("%s: error trying to create HTTP request: %w", transformName, err)
	}

	return func(r record.Record) (record.Record, error) {
		req, err := http.NewRequest(
			method,
			rawURL,
			bytes.NewReader(r.Payload.Bytes()),
		)
		if err != nil {
			return record.Record{}, cerrors.Errorf("%s: error trying to create HTTP request: %w", transformName, err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return record.Record{}, cerrors.Errorf("%s: error trying to execute HTTP request: %w", transformName, err)
		}
		defer resp.Body.Close()

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return record.Record{}, cerrors.Errorf("%s: error trying to read response body: %w", transformName, err)
		}

		r.Payload = record.RawData{Raw: body}
		return r, nil
	}, nil
}
