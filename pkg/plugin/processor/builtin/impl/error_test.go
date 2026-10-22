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

package impl

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestError_EmptyConfig(t *testing.T) {
	is := is.New(t)
	proc := NewErrorProcessor(log.Nop())
	cfg := config.Config{}
	ctx := context.Background()
	records := []opencdc.Record{
		{
			Metadata: map[string]string{"key1": "val1"},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
		{
			Metadata: map[string]string{"key2": "val2"},
			Payload:  opencdc.Change{},
		},
	}
	err := proc.Configure(ctx, cfg)
	is.NoErr(err)

	got := proc.Process(ctx, records)
	is.Equal(len(got), 2)
	for _, r := range got {
		is.Equal(r.(sdk.ErrorRecord).Error.Error(), "error processor triggered")
	}
}

func TestError_ErrorMessage(t *testing.T) {
	records := []opencdc.Record{
		{
			Metadata: map[string]string{"foo": "rec 1"},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
		{
			Metadata: map[string]string{"foo": "rec 2"},
			Payload:  opencdc.Change{},
		},
	}
	testCases := []struct {
		name            string
		cfg             config.Config
		wantErrMessages []string
	}{{
		name:            "static error message",
		cfg:             config.Config{"message": "static error message"},
		wantErrMessages: []string{"static error message", "static error message"},
	}, {
		name:            "template error message",
		cfg:             config.Config{"message": "error message: {{.Metadata.foo}}"},
		wantErrMessages: []string{"error message: rec 1", "error message: rec 2"},
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			proc := NewErrorProcessor(log.Nop())
			ctx := context.Background()

			err := proc.Configure(ctx, tc.cfg)
			is.NoErr(err)

			got := proc.Process(ctx, records)
			is.Equal(len(got), 2)
			for i, r := range got {
				is.Equal(r.(sdk.ErrorRecord).Error.Error(), tc.wantErrMessages[i])
			}
		})
	}
}

// A message template that fails to execute reports the failure, not the value
// the template function quoted.
func TestError_TemplateFailureDoesNotContainRecordValues(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	proc := NewErrorProcessor(log.Nop())
	is.NoErr(proc.Configure(ctx, config.Config{"message": `{{ mustToDate "2006-01-02" .Metadata.key }}`}))
	is.NoErr(proc.Open(ctx))

	out := proc.Process(ctx, []opencdc.Record{{Metadata: map[string]string{"key": "customer-SECRET-4711"}}})
	er, ok := out[0].(sdk.ErrorRecord)
	is.True(ok)
	is.True(strings.Contains(er.Error.Error(), "template execution failed"))
	is.True(!strings.Contains(er.Error.Error(), "SECRET"))
}
