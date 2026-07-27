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

package egress

import (
	"context"
	"strings"
	"testing"

	"github.com/matryer/is"
)

func TestEnvSecretResolver_Resolve(t *testing.T) {
	ctx := context.Background()

	t.Run("resolves a granted name from the namespaced env var", func(t *testing.T) {
		is := is.New(t)
		t.Setenv("CONDUIT_SECRET_OPENAI_KEY", "Bearer sk-live-123")
		got, err := EnvSecretResolver{}.Resolve(ctx, "openai_key")
		is.NoErr(err)
		is.Equal(got, "Bearer sk-live-123") // value used verbatim as the Authorization header
	})

	t.Run("normalizes punctuation in the name to underscores", func(t *testing.T) {
		is := is.New(t)
		t.Setenv("CONDUIT_SECRET_OPENAI_API_KEY", "Bearer x")
		// "openai.api-key" and "openai_api_key" both normalize to OPENAI_API_KEY.
		g1, err := EnvSecretResolver{}.Resolve(ctx, "openai.api-key")
		is.NoErr(err)
		is.Equal(g1, "Bearer x")
		g2, err := EnvSecretResolver{}.Resolve(ctx, "OpenAI_API_Key")
		is.NoErr(err)
		is.Equal(g2, "Bearer x")
	})

	t.Run("missing var is a non-silent error, not an empty value", func(t *testing.T) {
		is := is.New(t)
		// Ensure the var is absent.
		v, err := EnvSecretResolver{}.Resolve(ctx, "definitely_absent_secret")
		is.True(err != nil) // fails closed
		is.Equal(v, "")     // never returns an empty credential to send
	})

	t.Run("present-but-empty var is a non-silent error", func(t *testing.T) {
		is := is.New(t)
		t.Setenv("CONDUIT_SECRET_EMPTY_ONE", "")
		v, err := EnvSecretResolver{}.Resolve(ctx, "empty_one")
		is.True(err != nil) // an empty env value must not become an empty Authorization
		is.Equal(v, "")
	})

	t.Run("error names the expected env var but never echoes a value", func(t *testing.T) {
		is := is.New(t)
		_, err := EnvSecretResolver{}.Resolve(ctx, "some_key")
		is.True(err != nil)
		// Operator-actionable: tells them which var to set.
		is.True(strings.Contains(err.Error(), "CONDUIT_SECRET_SOME_KEY"))
	})
}

func TestNormalizeSecretName(t *testing.T) {
	is := is.New(t)
	is.Equal(normalizeSecretName("openai_key"), "OPENAI_KEY")
	is.Equal(normalizeSecretName("openai.api-key"), "OPENAI_API_KEY")
	is.Equal(normalizeSecretName("Voyage Key!"), "VOYAGE_KEY_")
	is.Equal(normalizeSecretName("k8s-secret/3"), "K8S_SECRET_3")
}
