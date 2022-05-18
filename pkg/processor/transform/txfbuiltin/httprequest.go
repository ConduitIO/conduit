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

	httpRequestConfigURL = "url"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(httpRequestName, transform.NewBuilder(HTTPRequest))
}

func HTTPRequest(config transform.Config) (transform.Transform, error) {
	return httpRequest(httpRequestName, recordPayloadGetSetter{}, config)
}

func httpRequest(
	transformName string,
	getSetter recordDataGetSetter,
	config transform.Config,
) (transform.Transform, error) {
	path := config[httpRequestConfigURL]
	if path == "" {
		return nil, cerrors.New("missing url")
	}

	_, err := url.Parse(path)
	if err != nil {
		return nil, err
	}

	return func(r record.Record) (record.Record, error) {
		data := getSetter.Get(r)

		req, err := http.NewRequest("GET", path, bytes.NewBuffer(data.Bytes()))
		if err != nil {
			return record.Record{}, err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return record.Record{}, err
		}
		defer resp.Body.Close()

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return record.Record{}, err
		}

		r = getSetter.Set(r, record.RawData{Raw: body})
		return r, nil
	}, nil
}
