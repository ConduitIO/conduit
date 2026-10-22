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

	"github.com/conduitio/conduit-commons/database/inmemory"
	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/env"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
	condprom "github.com/conduitio/conduit/pkg/foundation/metrics/prometheus"
	"github.com/google/uuid"
	"github.com/matryer/is"
	promclient "github.com/prometheus/client_golang/prometheus"
	"go.uber.org/mock/gomock"

	pmock "github.com/conduitio/conduit/pkg/plugin/connector/mock"
)

// startedConnector creates a source whose last active settings reference an
// environment variable that is not allowed (any more), as after the operator
// removed it from env.allow.
func startedConnector(t *testing.T, ctx context.Context, svc *Service) *Instance {
	t.Helper()
	conn, err := svc.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(),
		Config{Name: "pg", Settings: map[string]string{"url": "postgres://u:${PG_PASSWORD}@db/app"}}, ProvisionTypeAPI)
	if err != nil {
		t.Fatal(err)
	}
	conn.LastActiveConfig = Config{Name: "pg", Settings: map[string]string{"url": "postgres://u:${PG_PASSWORD}@db/app"}}
	return conn
}

// The plugin's deletion hook needs the settings; if they no longer resolve, a
// delete would leave behind what the hook cleans up (a replication slot keeps
// retaining WAL). It is refused with a stable code that names the setting and
// the fix, and nothing is removed.
func TestService_Delete_RefusedWhenTheDeleteHookCannotRun(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	svc := NewService(log.Nop(), &inmemory.DB{}, nil, WithSettingsResolver(EnvSettingsResolver{Allow: nil}))
	conn := startedConnector(t, ctx, svc)
	fetcher := fakePluginFetcher{conn.Plugin: pmock.NewDispenser(gomock.NewController(t))}

	err := svc.Delete(ctx, conn.ID, fetcher)
	ce, ok := conduiterr.Get(err)
	is.True(ok)
	is.Equal(ce.Code, CodeConnectorDeleteHookUnresolvable)
	is.Equal(ce.ConfigPath, "settings.url")
	is.True(strings.Contains(ce.Suggestion, "force"))
	is.True(strings.Contains(ce.Suggestion, "env.allow"))
	got, err := svc.Get(ctx, conn.ID)
	is.NoErr(err) // still there
	is.Equal(got, conn)
}

func TestService_Delete_ForceSkipsTheHookWithAWarningAndAMetric(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	reg := condprom.NewRegistry(nil)
	metrics.Register(reg)
	promReg := promclient.NewRegistry()
	is.NoErr(promReg.Register(reg))

	svc := NewService(log.Nop(), &inmemory.DB{}, nil, WithSettingsResolver(EnvSettingsResolver{Allow: nil}))
	conn := startedConnector(t, ctx, svc)
	// the plugin is torn down but the deletion hook is NOT called (no expectation)
	ctrl := gomock.NewController(t)
	src := pmock.NewSourcePlugin(ctrl)
	src.EXPECT().Teardown(gomock.Any(), gomock.Any()).Return(pconnector.SourceTeardownResponse{}, nil).AnyTimes()
	disp := pmock.NewDispenser(ctrl)
	disp.EXPECT().DispenseSource().Return(src, nil).AnyTimes()
	fetcher := fakePluginFetcher{conn.Plugin: disp}

	fctx, warnings := WithDeleteWarnings(WithForceDelete(ctx))
	is.NoErr(svc.Delete(fctx, conn.ID, fetcher))
	w := warnings.List()
	is.Equal(len(w), 1)
	is.True(strings.Contains(w[0], "settings.url"))
	is.True(strings.Contains(w[0], "by hand"))
	is.True(!strings.Contains(w[0], "PG_PASSWORD-VALUE"))
	_, err := svc.Get(ctx, conn.ID)
	is.True(err != nil) // deleted

	families, err := promReg.Gather()
	is.NoErr(err)
	var count float64
	for _, f := range families {
		if f.GetName() == "conduit_connector_force_deletes_total" {
			for _, m := range f.GetMetric() {
				count += m.GetCounter().GetValue()
			}
		}
	}
	is.Equal(count, float64(1))
}

// A delete whose settings resolve, or that was never started, is unaffected and
// needs no force.
func TestService_Delete_ResolvableOrNeverStartedNeedsNoForce(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	t.Setenv("PG_PASSWORD", "PG_PASSWORD-VALUE")
	allow, err := env.NewAllowlist([]string{"PG_PASSWORD"})
	is.NoErr(err)
	svc := NewService(log.Nop(), &inmemory.DB{}, nil, WithSettingsResolver(EnvSettingsResolver{Allow: allow}))
	conn := startedConnector(t, ctx, svc)
	ctrl := gomock.NewController(t)
	disp := pmock.NewDispenser(ctrl)
	disp.EXPECT().DispenseSource().Return(nil, context.Canceled).AnyTimes() // the hook runs (and may fail): not our concern here
	is.NoErr(svc.Delete(ctx, conn.ID, fakePluginFetcher{conn.Plugin: disp}))

	svc2 := NewService(log.Nop(), &inmemory.DB{}, nil, WithSettingsResolver(EnvSettingsResolver{Allow: nil}))
	fresh, err := svc2.Create(ctx, uuid.NewString(), TypeSource, "test-plugin", uuid.NewString(),
		Config{Name: "pg", Settings: map[string]string{"url": "postgres://u:${PG_PASSWORD}@db/app"}}, ProvisionTypeAPI)
	is.NoErr(err)
	is.NoErr(svc2.Delete(ctx, fresh.ID, fakePluginFetcher{fresh.Plugin: pmock.NewDispenser(ctrl)})) // never started: no hook
}
