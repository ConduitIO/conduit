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
	"testing"

	"github.com/conduitio/conduit-connector-protocol/cplugin"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/database/inmemory"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestDestination_NoLifecycleEvent(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, destinationMock := newTestDestination(ctx, t, ctrl)

	// assume that the same config was already active last time
	dest.Instance.LastActiveConfig = dest.Instance.Config

	_ = expectDestinationOpen(dest, destinationMock)

	// destination should not trigger any lifecycle event, because the config did not change

	err := dest.Open(ctx)
	is.NoErr(err)

	// after plugin is started the last active config is still the same
	is.Equal(dest.Instance.LastActiveConfig, dest.Instance.Config)
}

func TestDestination_LifecycleOnCreated_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, destinationMock := newTestDestination(ctx, t, ctrl)

	// before plugin is started we expect LastActiveConfig to be empty
	is.Equal(dest.Instance.LastActiveConfig, Config{})

	_ = expectDestinationOpen(dest, destinationMock)

	// destination should know it's the first run and trigger LifecycleOnCreated
	destinationMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		cplugin.DestinationLifecycleOnCreatedRequest{Config: dest.Instance.Config.Settings},
	).Return(cplugin.DestinationLifecycleOnCreatedResponse{}, nil)

	err := dest.Open(ctx)
	is.NoErr(err)

	// after plugin is started we expect LastActiveConfig to be set to Config
	is.Equal(dest.Instance.LastActiveConfig, dest.Instance.Config)
}

func TestDestination_LifecycleOnUpdated_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, destinationMock := newTestDestination(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	dest.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	_ = expectDestinationOpen(dest, destinationMock)

	// destination should know it was already run once with a different config and trigger LifecycleOnUpdated
	destinationMock.EXPECT().LifecycleOnUpdated(
		gomock.Any(),
		cplugin.DestinationLifecycleOnUpdatedRequest{
			ConfigBefore: dest.Instance.LastActiveConfig.Settings,
			ConfigAfter:  dest.Instance.Config.Settings,
		},
	).Return(cplugin.DestinationLifecycleOnUpdatedResponse{}, nil)

	err := dest.Open(ctx)
	is.NoErr(err)

	// after plugin is started we expect LastActiveConfig to be set to Config
	is.Equal(dest.Instance.LastActiveConfig, dest.Instance.Config)
}

func TestDestination_LifecycleOnCreated_Error(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, destinationMock := newTestDestination(ctx, t, ctrl)

	// before plugin is started we expect LastActiveConfig to be empty
	is.Equal(dest.Instance.LastActiveConfig, Config{})

	destinationMock.EXPECT().Configure(
		gomock.Any(),
		cplugin.DestinationConfigureRequest{Config: dest.Instance.Config.Settings},
	).Return(cplugin.DestinationConfigureResponse{}, nil)

	// destination should know it's the first run and trigger LifecycleOnCreated, but it fails
	want := cerrors.New("whoops")
	destinationMock.EXPECT().LifecycleOnCreated(
		gomock.Any(),
		cplugin.DestinationLifecycleOnCreatedRequest{Config: dest.Instance.Config.Settings},
	).Return(cplugin.DestinationLifecycleOnCreatedResponse{}, want)

	// destination should terminate plugin in case of an error
	destinationMock.EXPECT().Teardown(gomock.Any(), cplugin.DestinationTeardownRequest{}).Return(cplugin.DestinationTeardownResponse{}, nil)

	err := dest.Open(ctx)
	is.True(cerrors.Is(err, want))

	// after plugin is started we expect LastActiveConfig to be left unchanged
	is.Equal(dest.Instance.LastActiveConfig, Config{})
}

func TestDestination_LifecycleOnDeleted_Success(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, destinationMock := newTestDestination(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	dest.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	destinationMock.EXPECT().LifecycleOnDeleted(
		gomock.Any(),
		cplugin.DestinationLifecycleOnDeletedRequest{Config: dest.Instance.LastActiveConfig.Settings},
	).Return(cplugin.DestinationLifecycleOnDeletedResponse{}, nil)

	destinationMock.EXPECT().Teardown(gomock.Any(), cplugin.DestinationTeardownRequest{}).Return(cplugin.DestinationTeardownResponse{}, nil)

	err := dest.OnDelete(ctx)
	is.NoErr(err)
}

func TestDestination_LifecycleOnDeleted_BackwardsCompatibility(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, destinationMock := newTestDestination(ctx, t, ctrl)

	// assume that there was a config already active, but with different settings
	dest.Instance.LastActiveConfig.Settings = map[string]string{"last-active": "yes"}

	// we should ignore the error if the plugin does not implement lifecycle events
	destinationMock.EXPECT().LifecycleOnDeleted(
		gomock.Any(),
		cplugin.DestinationLifecycleOnDeletedRequest{Config: dest.Instance.LastActiveConfig.Settings},
	).Return(cplugin.DestinationLifecycleOnDeletedResponse{}, plugin.ErrUnimplemented)

	destinationMock.EXPECT().Teardown(gomock.Any(), cplugin.DestinationTeardownRequest{}).Return(cplugin.DestinationTeardownResponse{}, nil)

	err := dest.OnDelete(ctx)
	is.NoErr(err)
}

func TestDestination_LifecycleOnDeleted_Skip(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, _ := newTestDestination(ctx, t, ctrl)

	// assume that no config was active before, in that case deleted event
	// should be skipped
	dest.Instance.LastActiveConfig = Config{}

	err := dest.OnDelete(ctx)
	is.NoErr(err)
}

func newTestDestination(ctx context.Context, t *testing.T, ctrl *gomock.Controller) (*Destination, *mock.DestinationPlugin) {
	is := is.New(t)
	logger := log.Nop()
	db := &inmemory.DB{}
	persister := NewPersister(logger, db, DefaultPersisterDelayThreshold, 1)

	instance := &Instance{
		ID:   "test-connector-id",
		Type: TypeDestination,
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

	destinationMock := mock.NewDestinationPlugin(ctrl)
	pluginDispenser := mock.NewDispenser(ctrl)
	pluginDispenser.EXPECT().DispenseDestination().Return(destinationMock, nil).AnyTimes()

	conn, err := instance.Connector(ctx, fakePluginFetcher{instance.Plugin: pluginDispenser})
	is.NoErr(err)
	dest, ok := conn.(*Destination)
	is.True(ok)
	return dest, destinationMock
}

func expectDestinationOpen(dest *Destination, destinationMock *mock.DestinationPlugin) *builtin.InMemoryDestinationRunStream {
	stream := &builtin.InMemoryDestinationRunStream{}

	destinationMock.EXPECT().Configure(gomock.Any(),
		cplugin.DestinationConfigureRequest{
			Config: dest.Instance.Config.Settings,
		},
	).Return(cplugin.DestinationConfigureResponse{}, nil)
	destinationMock.EXPECT().Open(gomock.Any(), cplugin.DestinationOpenRequest{}).Return(cplugin.DestinationOpenResponse{}, nil)
	destinationMock.EXPECT().NewStream().Return(stream)
	destinationMock.EXPECT().Run(gomock.Any(), stream).Do(func(ctx context.Context, _ cplugin.DestinationRunStream) {
		stream.Init(ctx)
	}).Return(nil)

	return stream
}
