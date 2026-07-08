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

package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	existingDir := t.TempDir()

	tests := []struct {
		name     string
		req      Request
		wantCode conduiterr.Code
		wantErr  bool
	}{
		{
			name: "valid connector",
			req: Request{
				Kind: KindConnector, Language: LanguageGo, Name: "s3",
				Module: "github.com/devaris/conduit-connector-s3",
				Path:   filepath.Join(t.TempDir(), "out"),
			},
		},
		{
			name: "valid processor",
			req: Request{
				Kind: KindProcessor, Language: LanguageGo, Name: "uppercase",
				Module: "github.com/devaris/conduit-processor-uppercase",
				Path:   filepath.Join(t.TempDir(), "out"),
			},
		},
		{
			name:     "unsupported language python",
			req:      Request{Kind: KindConnector, Language: "python", Name: "s3", Module: "github.com/devaris/conduit-connector-s3"},
			wantErr:  true,
			wantCode: CodeUnsupportedLanguage,
		},
		{
			name:     "missing language",
			req:      Request{Kind: KindConnector, Name: "s3", Module: "github.com/devaris/conduit-connector-s3"},
			wantErr:  true,
			wantCode: CodeUnsupportedLanguage,
		},
		{
			name:     "missing name",
			req:      Request{Kind: KindConnector, Language: LanguageGo, Module: "github.com/devaris/conduit-connector-s3"},
			wantErr:  true,
			wantCode: CodeInvalidModule,
		},
		{
			name:     "name with hyphen is not a valid Go identifier",
			req:      Request{Kind: KindConnector, Language: LanguageGo, Name: "google-pubsub", Module: "github.com/devaris/conduit-connector-google-pubsub"},
			wantErr:  true,
			wantCode: CodeInvalidModule,
		},
		{
			name:     "missing module",
			req:      Request{Kind: KindConnector, Language: LanguageGo, Name: "s3"},
			wantErr:  true,
			wantCode: CodeInvalidModule,
		},
		{
			name:     "module suffix does not match name",
			req:      Request{Kind: KindConnector, Language: LanguageGo, Name: "s3", Module: "github.com/devaris/conduit-connector-postgres"},
			wantErr:  true,
			wantCode: CodeInvalidModule,
		},
		{
			name:     "module is for the wrong kind",
			req:      Request{Kind: KindConnector, Language: LanguageGo, Name: "s3", Module: "github.com/devaris/conduit-processor-s3"},
			wantErr:  true,
			wantCode: CodeInvalidModule,
		},
		{
			name:     "module missing github.com host",
			req:      Request{Kind: KindConnector, Language: LanguageGo, Name: "s3", Module: "gitlab.com/devaris/conduit-connector-s3"},
			wantErr:  true,
			wantCode: CodeInvalidModule,
		},
		{
			name: "destination exists without force",
			req: Request{
				Kind: KindConnector, Language: LanguageGo, Name: "s3",
				Module: "github.com/devaris/conduit-connector-s3",
				Path:   existingDir,
			},
			wantErr:  true,
			wantCode: CodeDestinationExists,
		},
		{
			name: "destination exists with force is allowed",
			req: Request{
				Kind: KindConnector, Language: LanguageGo, Name: "s3",
				Module: "github.com/devaris/conduit-connector-s3",
				Path:   existingDir,
				Force:  true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validate(tt.req)
			if !tt.wantErr {
				require.NoError(t, err)
				assert.NotEmpty(t, got.Path, "Path should be defaulted")
				return
			}

			require.Error(t, err)
			ce, ok := conduiterr.Get(err)
			require.True(t, ok, "error should be a *conduiterr.ConduitError")
			assert.Equal(t, tt.wantCode.Reason(), ce.Code.Reason())
			assert.NotEmpty(t, ce.Suggestion, "every validate error must carry a suggestion (CLI output conventions)")
		})
	}
}

func TestValidate_DefaultsPath(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	got, err := validate(Request{
		Kind: KindConnector, Language: LanguageGo, Name: "s3",
		Module: "github.com/devaris/conduit-connector-s3",
	})
	require.NoError(t, err)
	assert.Equal(t, "./conduit-connector-s3", got.Path)
}
