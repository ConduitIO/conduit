// Copyright © 2022 Meroxa, Inc.
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
	"sync"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/csync"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/matryer/is"
)

func TestSource_NoLifecycleEvent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// assume that the same config was already active last time
	src.Instance.LastActiveConfig = src.Instance.Config

	sourceMock.EXPECT().Configure(gomock.Any(), src.Instance.Config.Settings).Return(nil)
	sourceMock.EXPECT().Start(gomock.Any(), nil).Return(nil)

	// source should not trigger any lifecycle event, because the config did not change

	err := src.Open(ctx)
	is.NoErr(err)

	// after plugin is started the last active config is still the same
	is.Equal(src.Instance.LastActiveConfig, src.Instance.Config)
}

func TestSource_LifecycleOnCreated_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// before plugin is started we expect LastActiveConfig to be empty
	is.Equal(src.Instance.LastActiveConfig, Config{})

	sourceMock.EXPECT().Configure(gomock.Any(), src.Instance.Config.Settings).Return(nil)
	sourceMock.EXPECT().Start(gomock.Any(), nil).Return(nil)

	// source should know it's the first run and trigger LifecycleOnCreated
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), src.Instance.Config.Settings).Return(nil)

	err := src.Open(ctx)
	is.NoErr(err)

	// after plugin is started we expect LastActiveConfig to be set to Config
	is.Equal(src.Instance.LastActiveConfig, src.Instance.Config)
}

func TestSource_LifecycleOnUpdated_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	src.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	sourceMock.EXPECT().Configure(gomock.Any(), src.Instance.Config.Settings).Return(nil)
	sourceMock.EXPECT().Start(gomock.Any(), nil).Return(nil)

	// source should know it was already run once with a different config and trigger LifecycleOnUpdated
	sourceMock.EXPECT().LifecycleOnUpdated(gomock.Any(), src.Instance.LastActiveConfig.Settings, src.Instance.Config.Settings).Return(nil)

	err := src.Open(ctx)
	is.NoErr(err)

	// after plugin is started we expect LastActiveConfig to be set to Config
	is.Equal(src.Instance.LastActiveConfig, src.Instance.Config)
}

func TestSource_LifecycleOnCreated_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// before plugin is started we expect LastActiveConfig to be empty
	is.Equal(src.Instance.LastActiveConfig, Config{})

	sourceMock.EXPECT().Configure(gomock.Any(), src.Instance.Config.Settings).Return(nil)

	// source should know it's the first run and trigger LifecycleOnCreated, but it fails
	want := cerrors.New("whoops")
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), src.Instance.Config.Settings).Return(want)

	// source should terminate plugin in case of an error
	sourceMock.EXPECT().Teardown(gomock.Any()).Return(nil)

	err := src.Open(ctx)
	is.True(cerrors.Is(err, want))

	// after plugin is started we expect LastActiveConfig to be left unchanged
	is.Equal(src.Instance.LastActiveConfig, Config{})
}

func TestSource_LifecycleOnDeleted_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	src.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	sourceMock.EXPECT().LifecycleOnDeleted(gomock.Any(), src.Instance.LastActiveConfig.Settings).Return(nil)
	sourceMock.EXPECT().Teardown(gomock.Any()).Return(nil)

	err := src.OnDelete(ctx)
	is.NoErr(err)
}

func TestSource_LifecycleOnDeleted_BackwardsCompatibility(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	src.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	// we should ignore the error if the plugin does not implement lifecycle events
	sourceMock.EXPECT().LifecycleOnDeleted(gomock.Any(), src.Instance.LastActiveConfig.Settings).Return(plugin.ErrUnimplemented)
	sourceMock.EXPECT().Teardown(gomock.Any()).Return(nil)

	err := src.OnDelete(ctx)
	is.NoErr(err)
}

func TestSource_LifecycleOnDeleted_Skip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, _ := newTestSource(ctx, t, ctrl)

	// assume that no config was active before, in that case deleted event
	// should be skipped
	src.Instance.LastActiveConfig = Config{}

	err := src.OnDelete(ctx)
	is.NoErr(err)
}

func TestSource_Ack_Deadlock(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	src, sourceMock := newTestSource(ctx, t, ctrl)

	sourceMock.EXPECT().Configure(gomock.Any(), src.Instance.Config.Settings).Return(nil)
	sourceMock.EXPECT().LifecycleOnCreated(gomock.Any(), src.Instance.Config.Settings).Return(nil)
	sourceMock.EXPECT().Start(gomock.Any(), nil).Return(nil)
	sourceMock.EXPECT().Ack(gomock.Any(), record.Position("test-pos")).Return(nil).Times(5)

	err := src.Open(ctx)
	is.NoErr(err)

	const msgs = 5
	var wg sync.WaitGroup
	wg.Add(msgs)
	for i := 0; i < msgs; i++ {
		go func() {
			err := src.Ack(ctx, record.Position("test-pos"))
			wg.Done()
			is.NoErr(err)
		}()
	}

	err = (*csync.WaitGroup)(&wg).WaitTimeout(ctx, 100*time.Millisecond)
	is.NoErr(err)
}

func newTestSource(ctx context.Context, t *testing.T, ctrl *gomock.Controller) (*Source, *mock.SourcePlugin) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	persister := NewPersister(logger, db, DefaultPersisterDelayThreshold, 1)

	instance := &Instance{
		ID:   "test-connector-id",
		Type: TypeSource,
		Config: Config{
			Name: "test-name",
			Settings: map[string]string{
				"foo": "bar",
			},
		},
		PipelineID:    "test-pipeline-id",
		Plugin:        "test-plugin",
		ProvisionedBy: ProvisionTypeAPI,
	}
	instance.Init(logger, persister)

	sourceMock := mock.NewSourcePlugin(ctrl)
	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().DispenseSource().Return(sourceMock, nil).AnyTimes()

	conn, err := instance.Connector(ctx, fakePluginFetcher{instance.Plugin: pluginDispenser})
	is.NoErr(err)
	src, ok := conn.(*Source)
	is.True(ok)
	return src, sourceMock
}
