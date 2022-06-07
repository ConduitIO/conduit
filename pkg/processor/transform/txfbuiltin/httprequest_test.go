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
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conduitio/conduit/pkg/processor/transform"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/matryer/is"
)

func TestHTTPRequest_Build(t *testing.T) {
	type args struct {
		config transform.Config
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{{
		name:    "nil config returns error",
		args:    args{config: nil},
		wantErr: true,
	}, {
		name:    "empty config returns error",
		args:    args{config: map[string]string{}},
		wantErr: true,
	}, {
		name:    "empty url returns error",
		args:    args{config: map[string]string{httpRequestConfigURL: ""}},
		wantErr: true,
	}, {
		name:    "invalid url returns error",
		args:    args{config: map[string]string{httpRequestConfigURL: ":not/a/valid/url"}},
		wantErr: true,
	}, {
		name:    "invalid method returns error",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com", httpRequestConfigMethod: ":foo"}},
		wantErr: true,
	}, {
		name:    "invalid backoffRetry.count returns error",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com", httpRequestBackoffRetryCount: "not-a-number"}},
		wantErr: true,
	}, {
		name:    "invalid backoffRetry.min returns error",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com", httpRequestBackoffRetryCount: "1", httpRequestBackoffRetryMin: "not-a-duration"}},
		wantErr: true,
	}, {
		name:    "invalid backoffRetry.max returns error",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com", httpRequestBackoffRetryCount: "1", httpRequestBackoffRetryMax: "not-a-duration"}},
		wantErr: true,
	}, {
		name:    "invalid backoffRetry.factor returns error",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com", httpRequestBackoffRetryCount: "1", httpRequestBackoffRetryFactor: "not-a-number"}},
		wantErr: true,
	}, {
		name:    "valid url returns transform",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com"}},
		wantErr: false,
	}, {
		name:    "valid url and method returns transform",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com", httpRequestConfigMethod: "GET"}},
		wantErr: false,
	}, {
		name:    "invalid backoff retry config is ignored",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com", httpRequestBackoffRetryMin: "not-a-duration", httpRequestBackoffRetryMax: "not-a-duration", httpRequestBackoffRetryFactor: "not-a-number"}},
		wantErr: false,
	}, {
		name:    "valid url, method and backoff retry config returns transform",
		args:    args{config: map[string]string{httpRequestConfigURL: "http://example.com", httpRequestBackoffRetryCount: "1", httpRequestBackoffRetryMin: "10ms", httpRequestBackoffRetryMax: "1s", httpRequestBackoffRetryFactor: "1.3"}},
		wantErr: false,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := HTTPRequest(tt.args.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("HTTPRequest() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestHTTPRequest_TransformSuccess(t *testing.T) {
	respBody := []byte("foo-bar/response")

	type args struct {
		r record.Record
	}
	tests := []struct {
		name   string
		config transform.Config
		args   args
		want   record.Record
	}{{
		name:   "structured data",
		config: map[string]string{httpRequestConfigMethod: "GET"},
		args: args{r: record.Record{
			Payload: record.StructuredData{
				"bar": 123,
				"baz": nil,
			},
		}},
		want: record.Record{
			Payload: record.RawData{Raw: respBody},
		},
	}, {
		name:   "raw data",
		config: map[string]string{},
		args: args{r: record.Record{
			Payload: record.RawData{Raw: []byte("random data")},
		}},
		want: record.Record{
			Payload: record.RawData{Raw: respBody},
		},
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			wantMethod := tt.config[httpRequestConfigMethod]
			if wantMethod == "" {
				wantMethod = "POST" // default
			}
			wantBody := tt.args.r.Payload.Bytes()

			srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
				is.Equal(wantMethod, req.Method)

				gotBody, err := ioutil.ReadAll(req.Body)
				is.NoErr(err)
				is.Equal(wantBody, gotBody)

				_, err = resp.Write(respBody)
				is.NoErr(err)
			}))
			defer srv.Close()

			tt.config[httpRequestConfigURL] = srv.URL
			txfFunc, err := HTTPRequest(tt.config)
			is.NoErr(err)

			got, err := txfFunc(tt.args.r)
			is.NoErr(err)
			is.Equal(got.Payload, record.RawData{Raw: respBody})
		})
	}
}

func TestHTTPRequest_TransformRetrySuccess(t *testing.T) {
	is := is.New(t)

	respBody := []byte("foo-bar/response")

	wantMethod := "POST"
	wantBody := []byte("random data")

	srvHandlerCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		srvHandlerCount++

		is.Equal(wantMethod, req.Method)

		gotBody, err := ioutil.ReadAll(req.Body)
		is.NoErr(err)
		is.Equal(wantBody, gotBody)

		if srvHandlerCount < 5 {
			// first 4 requests will fail with an internal server error
			resp.WriteHeader(http.StatusInternalServerError)
		} else {
			_, err := resp.Write(respBody)
			is.NoErr(err)
		}
	}))
	defer srv.Close()

	config := map[string]string{
		httpRequestConfigURL:          srv.URL,
		httpRequestBackoffRetryCount:  "4",
		httpRequestBackoffRetryMin:    "5ms",
		httpRequestBackoffRetryMax:    "10ms",
		httpRequestBackoffRetryFactor: "1.2",
	}

	txfFunc, err := HTTPRequest(config)
	is.NoErr(err)

	got, err := txfFunc(record.Record{Payload: record.RawData{Raw: wantBody}})
	is.NoErr(err)
	is.Equal(got.Payload, record.RawData{Raw: respBody})
	is.Equal(srvHandlerCount, 5)
}

func TestHTTPRequest_TransformRetryFail(t *testing.T) {
	is := is.New(t)

	srvHandlerCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		srvHandlerCount++
		// all requests fail
		resp.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	config := map[string]string{
		httpRequestConfigURL:          srv.URL,
		httpRequestBackoffRetryCount:  "5",
		httpRequestBackoffRetryMin:    "5ms",
		httpRequestBackoffRetryMax:    "10ms",
		httpRequestBackoffRetryFactor: "1.2",
	}

	txfFunc, err := HTTPRequest(config)
	is.NoErr(err)

	got, err := txfFunc(record.Record{Payload: record.RawData{}})
	is.True(err != nil) // expected an error
	is.Equal(got, record.Record{})
	is.Equal(srvHandlerCount, 6) // expected 6 requests (1 regular and 5 retries)
}
