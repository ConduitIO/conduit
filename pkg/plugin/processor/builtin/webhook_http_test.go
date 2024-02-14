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
	"context"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matryer/is"
)

func TestHTTPRequest_Build(t *testing.T) {
	tests := []struct {
		name    string
		config  map[string]string
		wantErr bool
	}{
		{
			name:    "nil config returns error",
			config:  nil,
			wantErr: true,
		},
		{
			name:    "empty config returns error",
			config:  map[string]string{},
			wantErr: true,
		},
		{
			name: "empty url returns error",
			config: map[string]string{
				"url": "",
			},
			wantErr: true,
		},
		{
			name: "invalid url returns error",
			config: map[string]string{
				"url": ":not/a/valid/url",
			},
			wantErr: true,
		},
		{
			name: "invalid method returns error",
			config: map[string]string{
				"url":    "http://example.com",
				"method": ":foo",
			},
			wantErr: true,
		},
		{
			name: "invalid backoffRetry.count returns error",
			config: map[string]string{
				"url":                "http://example.com",
				"backoffRetry.count": "not-a-number",
			},
			wantErr: true,
		},
		{
			name: "invalid backoffRetry.min returns error",
			config: map[string]string{
				"url":                "http://example.com",
				"backoffRetry.count": "1",
				"backoffRetry.min":   "not-a-duration",
			},
			wantErr: true,
		},
		{
			name: "invalid backoffRetry.max returns error",
			config: map[string]string{
				"url":                "http://example.com",
				"backoffRetry.count": "1",
				"backoffRetry.max":   "not-a-duration",
			},
			wantErr: true,
		},
		{
			name: "invalid backoffRetry.factor returns error",
			config: map[string]string{
				"url":                 "http://example.com",
				"backoffRetry.count":  "1",
				"backoffRetry.factor": "not-a-number",
			},
			wantErr: true,
		},
		{
			name: "valid url returns processor",
			config: map[string]string{
				"url": "http://example.com",
			},
			wantErr: false,
		},
		{
			name: "valid url and method returns processor",
			config: map[string]string{
				"url":    "http://example.com",
				"method": "GET",
			},
			wantErr: false,
		},
		//{
		//	name: "invalid backoff retry config is ignored",
		//	config: map[string]string{
		//		"url":                 "http://example.com",
		//		"backoffRetry.min":    "not-a-duration",
		//		"backoffRetry.max":    "not-a-duration",
		//		"backoffRetry.factor": "not-a-number",
		//	},
		//	wantErr: false,
		//},
		{
			name: "valid url, method and backoff retry config returns processor",
			config: map[string]string{
				"url":                 "http://example.com",
				"backoffRetry.count":  "1",
				"backoffRetry.min":    "10ms",
				"backoffRetry.max":    "1s",
				"backoffRetry.factor": "1.3",
				"contentType":         "application/json",
			},
			wantErr: false,
		},
		{
			name: "invalid: same value of response.body and response.status",
			config: map[string]string{
				"url":             "http://example.com",
				"response.body":   ".Payload.After",
				"response.status": ".Payload.After",
			},
			wantErr: true,
		},
		{
			name: "valid response.body and response.status",
			config: map[string]string{
				"url":             "http://example.com",
				"response.body":   ".Payload.After",
				"response.status": `.Metadata["response.status"]`,
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			underTest := NewWebhookHTTP(log.Test(t))
			err := underTest.Configure(context.Background(), tc.config)
			if (err != nil) != tc.wantErr {
				t.Errorf("HTTPRequest() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestHTTPRequest_Success(t *testing.T) {
	respBody := []byte("foo-bar/response")

	tests := []struct {
		name   string
		config map[string]string
		args   []opencdc.Record
		want   []sdk.ProcessedRecord
	}{
		{
			name:   "structured data",
			config: map[string]string{"method": "GET"},
			args: []opencdc.Record{{
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"bar": 123,
						"baz": nil,
					},
				},
			}},
			want: []sdk.ProcessedRecord{sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.RawData(respBody),
				},
			},
			},
		},
		{
			name:   "raw data",
			config: map[string]string{},
			args: []opencdc.Record{{
				Payload: opencdc.Change{
					After: opencdc.RawData("random data"),
				},
			}},
			want: []sdk.ProcessedRecord{sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.RawData(respBody),
				},
			}},
		},
		{
			name: "custom field for response body and status",
			config: map[string]string{
				"response.body":   ".Payload.After.Body",
				"response.status": ".Payload.After.Status",
			},
			args: []opencdc.Record{{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"a key": "random data",
					},
				},
			}},
			want: []sdk.ProcessedRecord{sdk.SingleRecord{
				Payload: opencdc.Change{
					After: opencdc.StructuredData{
						"a key":  "random data",
						"Body":   opencdc.RawData(respBody),
						"Status": 200,
					},
				},
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			wantMethod := tc.config["method"]
			if wantMethod == "" {
				wantMethod = "POST" // default
			}

			wantBody := tc.args[0].Bytes()

			srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
				is.Equal(wantMethod, req.Method)

				gotBody, err := io.ReadAll(req.Body)
				is.NoErr(err)
				is.Equal(wantBody, gotBody)

				_, err = resp.Write(respBody)
				is.NoErr(err)
			}))
			defer srv.Close()

			tc.config["url"] = srv.URL
			underTest := NewWebhookHTTP(log.Test(t))
			err := underTest.Configure(context.Background(), tc.config)
			is.NoErr(err)

			got := underTest.Process(context.Background(), tc.args)
			diff := cmp.Diff(got, tc.want, cmpopts.IgnoreUnexported(sdk.SingleRecord{}))
			if diff != "" {
				t.Logf("mismatch (-want +got): %s", diff)
				t.Fail()
			}
		})
	}
}

func TestHTTPRequest_RetrySuccess(t *testing.T) {
	is := is.New(t)

	respBody := []byte("foo-bar/response")

	wantMethod := "POST"
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

	config := map[string]string{
		"url":                 srv.URL,
		"backoffRetry.count":  "4",
		"backoffRetry.min":    "5ms",
		"backoffRetry.max":    "10ms",
		"backoffRetry.factor": "1.2",
	}

	underTest := NewWebhookHTTP(log.Test(t))
	err := underTest.Configure(context.Background(), config)
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

func TestHTTPRequest_RetryFail(t *testing.T) {
	is := is.New(t)

	srvHandlerCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		srvHandlerCount++
		// all requests fail
		resp.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	config := map[string]string{
		"url":                 srv.URL,
		"backoffRetry.count":  "5",
		"backoffRetry.min":    "5ms",
		"backoffRetry.max":    "10ms",
		"backoffRetry.factor": "1.2",
	}

	underTest := NewWebhookHTTP(log.Test(t))
	err := underTest.Configure(context.Background(), config)
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

func TestHTTPRequest_FilterRecord(t *testing.T) {
	is := is.New(t)

	wantMethod := "POST"
	rec := []opencdc.Record{
		{Payload: opencdc.Change{After: opencdc.RawData("random data")}},
	}

	wantBody := rec[0].Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		is.Equal(wantMethod, req.Method)

		gotBody, err := io.ReadAll(req.Body)
		is.NoErr(err)
		is.Equal(wantBody, gotBody)

		resp.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	config := map[string]string{
		"url": srv.URL,
	}

	underTest := NewWebhookHTTP(log.Test(t))
	err := underTest.Configure(context.Background(), config)
	is.NoErr(err)

	got := underTest.Process(context.Background(), rec)
	is.Equal(got, []sdk.ProcessedRecord{sdk.FilterRecord{}})
}
