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
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/matryer/is"
)

func TestRenameField_Process(t *testing.T) {
	is := is.New(t)
	proc := renameField{
		mapping: map[string]string{".Metadata.key1": "newKey", ".Payload.After.foo": "newFoo"},
	}
	records := []opencdc.Record{
		{
			Metadata: map[string]string{"key1": "val1", "key2": "val2"},
			Payload: opencdc.Change{
				Before: nil,
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
	}
	want := sdk.SingleRecord{
		Metadata: map[string]string{"newKey": "val1", "key2": "val2"},
		Payload: opencdc.Change{
			Before: nil,
			After: opencdc.StructuredData{
				"newFoo": "bar",
			},
		},
	}
	output := proc.Process(context.Background(), records)
	is.True(len(output) == 1)
	is.Equal(output[0], want)
}

func TestRenameField_Configure(t *testing.T) {
	proc := renameField{}
	ctx := context.Background()
	testCases := []struct {
		name    string
		cfg     map[string]string
		wantErr bool
	}{
		{
			name:    "valid config",
			cfg:     map[string]string{"mapping": ".Payload.After.foo:bar"},
			wantErr: false,
		}, {
			name:    "invalid config, contains a top-level reference",
			cfg:     map[string]string{"mapping": ".Metadata:foo,.Payload.After.foo:bar"},
			wantErr: true,
		}, {
			name:    "mapping param is missing",
			cfg:     map[string]string{},
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
