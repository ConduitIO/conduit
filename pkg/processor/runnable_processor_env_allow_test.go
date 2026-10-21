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

package processor

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/foundation/env"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"

	proc_mock "github.com/conduitio/conduit/pkg/plugin/processor/mock"
)

func TestRunnableProcessor_Open_ExpandsOnlyAllowedEnv(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("ALLOWED_TOKEN", "tok-1")
	t.Setenv("OTHER_SECRET", "nope")

	var got config.Config
	p := proc_mock.NewProcessor(gomock.NewController(t))
	p.EXPECT().Configure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, c config.Config) error {
		got = c
		return nil
	})
	p.EXPECT().Open(gomock.Any()).Return(nil)

	allow, err := env.NewAllowlist([]string{"ALLOWED_TOKEN"})
	is.NoErr(err)
	i := newTestInstance()
	i.Config.Settings = map[string]string{"headers.Authorization": "Bearer ${ALLOWED_TOKEN}"}
	underTest := newRunnableProcessor(p, nil, i)
	underTest.envAllow = allow
	is.NoErr(underTest.Open(ctx))
	is.Equal(got["headers.Authorization"], "Bearer tok-1")

	// the same runnable still refuses a name that is not allowed
	i2 := newTestInstance()
	i2.Config.Settings = map[string]string{"x": "${OTHER_SECRET}"}
	r2 := newRunnableProcessor(p, nil, i2)
	r2.envAllow = allow
	is.True(r2.Open(ctx) != nil)
}

// The Service hands its allowlist to every runnable processor it builds.
func TestService_WithEnvAllowlist(t *testing.T) {
	is := is.New(t)
	allow, err := env.NewAllowlist([]string{"A"})
	is.NoErr(err)
	s := NewService(log.Nop(), nil, nil, WithEnvAllowlist(allow))
	is.True(s.envAllow.Allows("A"))
	is.True(!s.envAllow.Allows("B"))
	is.True(NewService(log.Nop(), nil, nil).envAllow == nil) // default: nothing allowed
}
