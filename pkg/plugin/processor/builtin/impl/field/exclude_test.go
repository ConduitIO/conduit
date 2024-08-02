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

package field

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
)

func TestExcludeFields_Process(t *testing.T) {
	is := is.New(t)
	proc := NewExcludeProcessor(log.Nop())
	cfg := config.Config{"fields": ".Metadata,.Payload.After.foo"}
	ctx := context.Background()
	records := []opencdc.Record{
		{
			Metadata: map[string]string{"key1": "val1", "key2": "val2"},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo":  "bar",
					"keep": "me",
				},
			},
		},
	}
	want := sdk.SingleRecord{
		Metadata: map[string]string{},
		Payload: opencdc.Change{
			After: opencdc.StructuredData{
				"keep": "me",
			},
		},
	}
	err := proc.Configure(ctx, cfg)
	is.NoErr(err)
	output := proc.Process(context.Background(), records)
	is.True(len(output) == 1)
	is.Equal(output[0], want)
}

func TestExcludeField_Configure(t *testing.T) {
	proc := NewExcludeProcessor(log.Nop())
	ctx := context.Background()
	testCases := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     config.Config{"fields": ".Metadata,.Payload"},
			wantErr: false,
		}, {
			name:    "missing parameter",
			cfg:     config.Config{},
			wantErr: true,
		}, {
			name:    "cannot exclude .Operation",
			cfg:     config.Config{"fields": ".Operation"},
			wantErr: true,
		}, {
			name:    "cannot exclude .Position",
			cfg:     config.Config{"fields": ".Position"},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			err := proc.Configure(ctx, tc.cfg)
			if tc.wantErr {
				is.True(err != nil)
				return
			}
			is.NoErr(err)
		})
	}
}
