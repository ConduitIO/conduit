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

package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/matryer/is"
)

func TestHTTPProcessor_Configure(t *testing.T) {
	tests := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name:    "empty config returns error",
			config:  config.Config{},
			wantErr: `failed parsing configuration: config invalid: error validating "request.url": required parameter is not provided`,
		},
		{
			name: "empty url returns error",
			config: config.Config{
				"request.url": "",
			},
			wantErr: `failed parsing configuration: config invalid: error validating "request.url": required parameter is not provided`,
		},
		{
			name: "invalid url returns error",
			config: config.Config{
				"request.url": ":not/a/valid/url",
			},
			wantErr: "configuration check failed: parse \":not/a/valid/url\": missing protocol scheme",
		},
		{
			name: "invalid method returns error",
			config: config.Config{
				"request.url":    "http://example.com",
				"request.method": ":foo",
			},
			wantErr: "configuration check failed: net/http: invalid method \":foo\"",
		},
		{
			name: "invalid backoffRetry.count returns error",
			config: config.Config{
				"request.url":        "http://example.com",
				"backoffRetry.count": "not-a-number",
			},
			wantErr: `failed parsing configuration: config invalid: error validating "backoffRetry.count": "not-a-number" value is not a float: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.min returns error",
			config: config.Config{
				"request.url":        "http://example.com",
				"backoffRetry.count": "1",
				"backoffRetry.min":   "not-a-duration",
			},
			wantErr: `failed parsing configuration: config invalid: error validating "backoffRetry.min": "not-a-duration" value is not a duration: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.max returns error",
			config: config.Config{
				"request.url":        "http://example.com",
				"backoffRetry.count": "1",
				"backoffRetry.max":   "not-a-duration",
			},
			wantErr: `failed parsing configuration: config invalid: error validating "backoffRetry.max": "not-a-duration" value is not a duration: invalid parameter type`,
		},
		{
			name: "invalid backoffRetry.factor returns error",
			config: config.Config{
				"request.url":         "http://example.com",
				"backoffRetry.count":  "1",
				"backoffRetry.factor": "not-a-number",
			},
			wantErr: `failed parsing configuration: config invalid: error validating "backoffRetry.factor": "not-a-number" value is not a float: invalid parameter type`,
		},
		{
			name: "valid url returns processor",
			config: config.Config{
				"request.url": "http://example.com",
			},
			wantErr: "",
		},
		{
			name: "valid url template returns processor",
			config: config.Config{
				"request.url": "http://example.com/{{.Payload.After}}",
			},
			wantErr: "",
		},
		{
			name: "invalid url template with a hyphen",
			config: config.Config{
				"request.url": "http://example.com/{{.Payload.After.my-key}}",
			},
			wantErr: "error while parsing the URL template: template: :1: bad character U+002D '-'",
		},
		{
			name: "valid url and method returns processor",
			config: config.Config{
				"request.url":    "http://example.com",
				"request.method": "GET",
			},
			wantErr: "",
		},
		{
			name: "valid url, method and backoff retry config returns processor",
			config: config.Config{
				"request.url":         "http://example.com",
				"request.contentType": "application/json",
				"backoffRetry.count":  "1",
				"backoffRetry.min":    "10ms",
				"backoffRetry.max":    "1s",
				"backoffRetry.factor": "1.3",
			},
			wantErr: "",
		},
		{
			name: "content-type header",
			config: config.Config{
				"request.url":          "http://example.com",
				"headers.content-type": "application/json",
			},
			wantErr: "",
		},
		{
			name: "invalid: content-type header and request.contentType",
			config: config.Config{
				"request.url":          "http://example.com",
				"request.contentType":  "application/json",
				"headers.content-type": "application/json",
			},
			wantErr: `configuration error, cannot provide both "request.contentType" and "headers.Content-Type", use "headers.Content-Type" only`,
		},
		{
			name: "invalid: same value of response.body and response.status",
			config: config.Config{
				"request.url":     "http://example.com",
				"response.body":   ".Payload.After",
				"response.status": ".Payload.After",
			},
			wantErr: "invalid configuration: response.body and response.status set to same field",
		},
		{
			name: "valid response.body and response.status",
			config: config.Config{
				"request.url":     "http://example.com",
				"response.body":   ".Payload.After",
				"response.status": `.Metadata["response.status"]`,
			},
			wantErr: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			underTest := NewHTTPProcessor(log.Test(t))
			err := underTest.Configure(context.Background(), tc.config)
			if tc.wantErr == "" {
				is.NoErr(err)
			} else {
				is.True(err != nil)
				is.Equal(tc.wantErr, err.Error())
			}
		})
	}
}

func TestHTTPProcessor_Success(t *testing.T) {
	respBody := []byte("foo-bar/response")

	tests := []struct {
		name     string
		config   config.Config
		status   int
		record   opencdc.Record
		wantBody string
		want     sdk.ProcessedRecord
	}{
		{
			name: "structured data",
			config: config.Config{
				"request.method": "POST",
				"request.body":   "{{ toJson . }}",
			},
			status: 200,
			record: opencdc.Record{
				Operation: opencdc.OperationCreate,
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"bar": 123,
						"baz": nil,
					},
				},
			},
			wantBody: `{"position":null,"operation":"create","metadata":null,"key":null,"payload":{"before":null,"after":{"bar":123,"baz":null}}}`,
			want: sdk.SingleRecord{
				Operation: opencdc.OperationCreate,
				Payload: opencdc.Change{
					After: opencdc.RawData(respBody),
				},
			},
		},
		{
			name: "raw data",
			config: config.Config{
				"request.method": "GET",
				"request.body":   "{{ toJson . }}",
			},
			status: 200,
			record: opencdc.Record{
				Operation: opencdc.OperationUpdate,
				Payload: opencdc.Change{
					After: opencdc.RawData("random data"),
				},
			},
			wantBody: `{"position":null,"operation":"update","metadata":null,"key":null,"payload":{"before":null,"after":"cmFuZG9tIGRhdGE="}}`,
			want: sdk.SingleRecord{
				Operation: opencdc.OperationUpdate,
				Payload: opencdc.Change{
					After: opencdc.RawData(respBody),
				},
			},
		},
		{
			name: "custom field for response body and status",
			config: config.Config{
				"response.body":   ".Payload.After.body",
				"response.status": ".Payload.After.status",
				"request.method":  "POST",
				"request.body":    "{{ toJson . }}",
			},
			status: 404,
			record: opencdc.Record{
				Operation: opencdc.OperationSnapshot,
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"a key": "random data",
					},
				},
			},
			wantBody: `{"position":null,"operation":"snapshot","metadata":null,"key":null,"payload":{"before":null,"after":{"a key":"random data"}}}`,
			want: sdk.SingleRecord{
				Operation: opencdc.OperationSnapshot,
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"a key":  "random data",
						"body":   respBody,
						"status": "404",
					},
				},
			},
		},
		{
			name: "request body: custom field, structured",
			config: config.Config{
				"request.body":   "{{ toJson . }}",
				"response.body":  ".Payload.After.httpResponse",
				"request.method": "POST",
			},
			status: 200,
			record: opencdc.Record{
				Operation: opencdc.OperationDelete,
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"before-key": "before-data",
					},
					After: opencdc.StructuredData{
						"after-key": "after-data",
					},
				},
			},
			wantBody: `{"position":null,"operation":"delete","metadata":null,"key":null,"payload":{"before":{"before-key":"before-data"},"after":{"after-key":"after-data"}}}`,
			want: sdk.SingleRecord{
				Operation: opencdc.OperationDelete,
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"before-key": "before-data",
					},
					After: opencdc.StructuredData{
						"after-key":    "after-data",
						"httpResponse": []byte("foo-bar/response"),
					},
				},
			},
		},
		{
			name: "request body: custom field, raw data",
			config: config.Config{
				"request.body":   `{{ printf "%s" .Payload.Before }}`,
				"response.body":  ".Payload.After.httpResponse",
				"request.method": "POST",
			},
			status: 200,
			record: opencdc.Record{
				Payload: opencdc.Change{
					Before: opencdc.RawData("uncooked data"),
					After: opencdc.StructuredData{
						"after-key": "after-data",
					},
				},
			},
			wantBody: `uncooked data`,
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					Before: opencdc.RawData("uncooked data"),
					After: opencdc.StructuredData{
						"after-key":    "after-data",
						"httpResponse": []byte("foo-bar/response"),
					},
				},
			},
		},
		{
			name: "request body: static",
			config: config.Config{
				"request.body":   `foo`,
				"response.body":  ".Payload.After.httpResponse",
				"request.method": "POST",
			},
			status: 200,
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"unused": "data",
					},
				},
			},
			wantBody: `foo`,
			want: sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"unused":       "data",
						"httpResponse": []byte("foo-bar/response"),
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			wantMethod := tc.config["request.method"]
			if wantMethod == "" {
				wantMethod = "GET" // default
			}

			srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
				is.Equal(wantMethod, req.Method)

				gotBody, err := io.ReadAll(req.Body)
				is.NoErr(err)
				is.Equal(tc.wantBody, string(gotBody))

				resp.WriteHeader(tc.status)
				_, err = resp.Write(respBody)
				is.NoErr(err)
			}))
			defer srv.Close()

			tc.config["request.url"] = srv.URL
			underTest := NewHTTPProcessor(log.Test(t))
			err := underTest.Configure(ctx, tc.config)
			is.NoErr(err)

			got := underTest.Process(ctx, []opencdc.Record{tc.record})
			is.Equal(
				"",
				cmp.Diff(
					[]sdk.ProcessedRecord{tc.want},
					got,
					cmpopts.IgnoreUnexported(sdk.SingleRecord{}),
				),
			)
		})
	}
}

func TestHTTPProcessor_URLTemplate(t *testing.T) {
	tests := []struct {
		name     string
		pathTmpl string // will be attached to the URL
		path     string // expected result of the pathTmpl
		record   opencdc.Record
	}{
		{
			name:     "URL template, success",
			pathTmpl: "/{{.Payload.After.foo}}",
			path:     "/123",
			record: opencdc.Record{
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"foo": 123,
					},
				},
			},
		},
		{
			name:     "URL template, key with a hyphen",
			pathTmpl: `/{{index .Payload.After "foo-bar"}}`,
			path:     "/baz",
			record: opencdc.Record{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"foo-bar": "baz",
					},
				},
			},
		},
		{
			name:     "URL template, path and query have spaces",
			pathTmpl: `/{{.Payload.Before.url}}`,
			path:     "/what%20is%20conduit?id=my+id",
			record: opencdc.Record{
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"url": "what is conduit?id=my id",
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
				// check the expected path with the evaluated URL
				is.Equal(req.URL.String(), tc.path)
			}))
			defer srv.Close()

			cfg := config.Config{
				// attach the path template to the URL
				"request.url": srv.URL + tc.pathTmpl,
			}
			underTest := NewHTTPProcessor(log.Test(t))
			err := underTest.Configure(context.Background(), cfg)
			is.NoErr(err)

			got := underTest.Process(context.Background(), []opencdc.Record{tc.record})
			is.Equal(1, len(got))
			_, ok := got[0].(sdk.SingleRecord)
			is.True(ok)
		})
	}
}

func TestHTTPProcessor_RetrySuccess(t *testing.T) {
	is := is.New(t)

	respBody := []byte("foo-bar/response")

	wantMethod := "GET"
	rec := []opencdc.Record{
		{Payload: opencdc.Change{After: opencdc.RawData("random data")}},
	}
	wantBody := rec[0].Bytes()

	srvHandlerCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		srvHandlerCount++

		is.Equal(wantMethod, req.Method)

		gotBody, err := io.ReadAll(req.Body)
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

	cfg := config.Config{
		"request.url":         srv.URL,
		"backoffRetry.count":  "4",
		"backoffRetry.min":    "5ms",
		"backoffRetry.max":    "10ms",
		"backoffRetry.factor": "1.2",
		"request.body":        "{{ toJson . }}",
	}

	underTest := NewHTTPProcessor(log.Test(t))
	err := underTest.Configure(context.Background(), cfg)
	is.NoErr(err)

	got := underTest.Process(context.Background(), rec)
	is.Equal(
		got,
		[]sdk.ProcessedRecord{sdk.SingleRecord{
			Payload: opencdc.Change{
				After: opencdc.RawData(respBody),
			},
		}},
	)
	is.Equal(srvHandlerCount, 5)
}

func TestHTTPProcessor_RetryFail(t *testing.T) {
	is := is.New(t)

	srvHandlerCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		srvHandlerCount++
		// all requests fail
		resp.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := config.Config{
		"request.url":         srv.URL,
		"backoffRetry.count":  "5",
		"backoffRetry.min":    "5ms",
		"backoffRetry.max":    "10ms",
		"backoffRetry.factor": "1.2",
	}

	underTest := NewHTTPProcessor(log.Test(t))
	err := underTest.Configure(context.Background(), cfg)
	is.NoErr(err)

	got := underTest.Process(
		context.Background(),
		[]opencdc.Record{{Payload: opencdc.Change{After: opencdc.RawData("something")}}},
	)
	is.Equal(1, len(got))
	_, isErr := got[0].(sdk.ErrorRecord)
	is.True(isErr)               // expected an error
	is.Equal(srvHandlerCount, 6) // expected 6 requests (1 regular and 5 retries)
}
