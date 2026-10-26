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
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"

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

// With an allowlisted reference, lifecycle events carry the resolved value, the
// same one Configure gets.
func TestSource_OnDelete_CarriesResolvedSettings(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("ALLOWED_PASSWORD", "pw-1")

	src, sourceMock := newTestSource(ctx, t, gomock.NewController(t))
	allow, err := env.NewAllowlist([]string{"ALLOWED_PASSWORD"})
	is.NoErr(err)
	src.Instance.settingsResolver = EnvSettingsResolver{Allow: allow}
	src.Instance.LastActiveConfig.Settings = map[string]string{"password": "${ALLOWED_PASSWORD}"}

	var got map[string]string
	sourceMock.EXPECT().LifecycleOnDeleted(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, req pconnector.SourceLifecycleOnDeletedRequest) (pconnector.SourceLifecycleOnDeletedResponse, error) {
			got = req.Config
			return pconnector.SourceLifecycleOnDeletedResponse{}, nil
		})
	sourceMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	is.NoErr(src.OnDelete(ctx))
	is.Equal(got, map[string]string{"password": "pw-1"})
}

// A setting that no longer resolves must not make the connector undeletable:
// the "deleted" event is skipped (the plugin is not told) and OnDelete succeeds.
// Created and updated events still fail on it.
func TestSource_OnDelete_UnresolvableSettingsDoNotBlockDeletion(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("NOT_ALLOWED_ANYMORE", "secret-value")

	src, sourceMock := newTestSource(ctx, t, gomock.NewController(t))
	allow, err := env.NewAllowlist([]string{"SOMETHING_ELSE"})
	is.NoErr(err)
	src.Instance.settingsResolver = EnvSettingsResolver{Allow: allow}
	src.Instance.LastActiveConfig.Settings = map[string]string{"password": "${NOT_ALLOWED_ANYMORE}"}

	// LifecycleOnDeleted must not be called; Teardown still is
	sourceMock.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil)
	is.NoErr(src.OnDelete(ctx))

	// an update with the same unresolvable setting is still refused
	_, err = src.triggerLifecycleEvent(ctx, map[string]string{"password": "${NOT_ALLOWED_ANYMORE}"}, map[string]string{"password": "${NOT_ALLOWED_ANYMORE}", "x": "1"})
	is.True(err != nil)
}

// Errors of the plugin that echo an expanded value reach the writer of the
// settings: the instance replaces the value by its reference, keeps the code,
// and errors.Is still works on the original.
func TestInstance_PluginErrorsDoNotEchoExpandedValues(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("DB_PASSWORD", "hunter2-SECRET-VALUE")

	src, sourceMock := newTestSource(ctx, t, gomock.NewController(t))
	allow, err := env.NewAllowlist([]string{"DB_PASSWORD"})
	is.NoErr(err)
	src.Instance.settingsResolver = EnvSettingsResolver{Allow: allow}
	src.Instance.Config.Settings = map[string]string{"url": "postgres://u:${DB_PASSWORD}@db/app"}
	src.plugin = sourceMock

	sentinel := cerrors.New("sentinel")
	coded := conduiterr.New(conduiterr.CodeInvalidArgument, "bad url postgres://u:hunter2-SECRET-VALUE@db/app")
	coded.Suggestion = "check hunter2-SECRET-VALUE"
	sourceMock.EXPECT().Configure(gomock.Any(), gomock.Any()).Return(pconnector.SourceConfigureResponse{}, cerrors.Errorf("connect postgres://u:hunter2-SECRET-VALUE@db/app: %w", sentinel))
	err = src.configure(ctx)
	is.True(err != nil)
	is.True(!strings.Contains(err.Error(), "SECRET-VALUE"))
	is.True(strings.Contains(err.Error(), "${DB_PASSWORD}"))
	is.True(cerrors.Is(err, sentinel)) // the classification survives

	sourceMock.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.SourceOpenResponse{}, coded)
	err = src.open(ctx)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, conduiterr.CodeInvalidArgument)
	is.True(!strings.Contains(ce.Message+ce.Suggestion+err.Error(), "SECRET-VALUE"))
	is.True(strings.Contains(ce.Message, "${DB_PASSWORD}"))

	// an error without the value is returned as it is
	plain := cerrors.New("connection refused")
	sourceMock.EXPECT().Open(gomock.Any(), gomock.Any()).Return(pconnector.SourceOpenResponse{}, plain)
	is.True(cerrors.Is(src.open(ctx), plain))
}
