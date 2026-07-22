// Copyright © 2026 Meroxa, Inc.
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

package toproto_test

import (
	"testing"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/http/api/toproto"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/matryer/is"
)

// Sentinel secret values planted in Settings across this file's tests
// (#2640). If any of these ever shows up in a toproto-built API response,
// a redaction site regressed.
const (
	redactTestDBPasswordSentinel = "SENTINEL_db_pw_9f3a2c"
	redactTestSASLSentinel       = "SENTINEL_sasl_7b1e4d"
	redactTestAccessKeySentinel  = "SENTINEL_access_key_44ff"
)

// TestConnectorConfig_RedactsSettings is the regression test for the #2640
// connector leak site (pkg/http/api/toproto/connector.go): ConnectorConfig
// must never echo back a Settings value, only "***", while keeping every key
// visible and leaving Name untouched.
func TestConnectorConfig_RedactsSettings(t *testing.T) {
	is := is.New(t)

	in := connector.Config{
		Name: "my-postgres-source",
		Settings: map[string]string{
			"url":           redactTestDBPasswordSentinel,
			"sasl.password": redactTestSASLSentinel,
		},
	}

	got := toproto.ConnectorConfig(in)

	is.Equal(got.Name, "my-postgres-source") // structural field must be untouched
	is.Equal(len(got.Settings), 2)
	for _, v := range got.Settings {
		is.True(v != redactTestDBPasswordSentinel) // secret leaked
		is.True(v != redactTestSASLSentinel)       // secret leaked
		is.Equal(v, log.Redacted)
	}
	_, hasURL := got.Settings["url"]
	_, hasSASL := got.Settings["sasl.password"]
	is.True(hasURL)  // key must stay visible
	is.True(hasSASL) // key must stay visible

	// input must not be mutated by the conversion
	is.Equal(in.Settings["url"], redactTestDBPasswordSentinel)
	is.Equal(in.Settings["sasl.password"], redactTestSASLSentinel)
}

func TestConnectorConfig_NilSettings(t *testing.T) {
	is := is.New(t)

	got := toproto.ConnectorConfig(connector.Config{Name: "no-settings"})
	is.Equal(got.Name, "no-settings")
	is.Equal(got.Settings, nil)
}

// TestProcessorConfig_RedactsSettings is the regression test for the #2640
// processor leak site (pkg/http/api/toproto/processor.go): ProcessorConfig
// must never echo back a Settings value (e.g. an HTTP processor's API key),
// only "***", while leaving Workers untouched.
func TestProcessorConfig_RedactsSettings(t *testing.T) {
	is := is.New(t)

	in := processor.Config{
		Settings: map[string]string{
			"request.headers.Authorization": redactTestAccessKeySentinel,
		},
		Workers: 4,
	}

	got := toproto.ProcessorConfig(in)

	is.Equal(got.Workers, int32(4)) // structural field must be untouched
	is.Equal(len(got.Settings), 1)
	is.Equal(got.Settings["request.headers.Authorization"], log.Redacted)
	is.True(got.Settings["request.headers.Authorization"] != redactTestAccessKeySentinel)
}

// TestPipelineDLQ_RedactsSettings is the regression test for the #2640 DLQ
// leak site (pkg/http/api/toproto/pipeline.go PipelineDLQ, the exact site
// filed in the issue): the DLQ destination's Settings must never be echoed
// back, only "***", while Plugin/WindowSize/WindowNackThreshold stay
// untouched.
func TestPipelineDLQ_RedactsSettings(t *testing.T) {
	is := is.New(t)

	in := pipeline.DLQ{
		Plugin: "builtin:s3",
		Settings: map[string]string{
			"aws.secretAccessKey": redactTestAccessKeySentinel,
		},
		WindowSize:          10,
		WindowNackThreshold: 5,
	}

	got := toproto.PipelineDLQ(in)

	is.Equal(got.Plugin, "builtin:s3") // structural field must be untouched
	is.Equal(got.WindowSize, uint64(10))
	is.Equal(got.WindowNackThreshold, uint64(5))
	is.Equal(len(got.Settings), 1)
	is.Equal(got.Settings["aws.secretAccessKey"], log.Redacted)
	is.True(got.Settings["aws.secretAccessKey"] != redactTestAccessKeySentinel)
}
