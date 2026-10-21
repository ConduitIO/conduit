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

package connector

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/env"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/google/uuid"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestSource_Configure_ExpandsAllowedEnvOnly(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("ALLOWED_PASSWORD", "pw-1")
	t.Setenv("DENIED_SECRET", "nope")

	src, sourceMock := newTestSource(ctx, t, gomock.NewController(t))
	allow, err := env.NewAllowlist([]string{"ALLOWED_PASSWORD"})
	is.NoErr(err)
	src.Instance.settingsResolver = EnvSettingsResolver{Allow: allow}
	src.Instance.Config.Settings = map[string]string{"password": "${ALLOWED_PASSWORD}", "plain": "x"}

	var got map[string]string
	sourceMock.EXPECT().Configure(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req pconnector.SourceConfigureRequest) (pconnector.SourceConfigureResponse, error) {
			got = req.Config
			return pconnector.SourceConfigureResponse{}, nil
		})
	stream := &builtin.InMemorySourceRunStream{}
	sourceMock.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.SourceOpenResponse{}, nil)
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), gomock.Any()).Return(pconnector.SourceLifecycleOnCreatedResponse{}, nil)
	sourceMock.EXPECT().NewStream().Return(stream)
	sourceMock.EXPECT().Run(gomock.Any(), stream).DoAndReturn(func(ctx context.Context, _ pconnector.SourceRunStream) error {
		stream.Init(ctx)
		return nil
	})
	is.NoErr(src.Open(ctx))
	is.Equal(got, map[string]string{"password": "pw-1", "plain": "x"})

	// the stored settings keep the reference: nothing secret is persisted
	is.Equal(src.Instance.Config.Settings["password"], "${ALLOWED_PASSWORD}")
}

// The Service hands its resolver to every instance it creates or loads.
func TestService_SettingsResolverReachesInstances(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	db := &inmemory.DB{}
	persister := NewPersister(log.Nop(), db, DefaultPersisterDelayThreshold, 1)

	allow, _ := env.NewAllowlist([]string{"A"})
	r := EnvSettingsResolver{Allow: allow}
	svc := NewService(log.Nop(), db, persister, WithSettingsResolver(r))

	i, err := svc.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(), Config{Name: "n"}, ProvisionTypeAPI)
	is.NoErr(err)
	got, ok := i.settingsResolver.(EnvSettingsResolver)
	is.True(ok)
	is.True(got.Allow.Allows("A"))

	// loaded from the store on restart
	svc2 := NewService(log.Nop(), db, persister, WithSettingsResolver(r))
	is.NoErr(svc2.Init(ctx))
	loaded, err := svc2.Get(ctx, i.ID)
	is.NoErr(err)
	_, ok = loaded.settingsResolver.(EnvSettingsResolver)
	is.True(ok)

	// no option: fail closed, nothing expands
	svc3 := NewService(log.Nop(), db, persister)
	i3, err := svc3.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(), Config{Name: "n"}, ProvisionTypeAPI)
	is.NoErr(err)
	t.Setenv("A", "x")
	_, err = i3.resolveSettings(map[string]string{"k": "${A}"})
	is.True(err != nil)
}
