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

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/conduitio/conduit/pkg/plugin/connector/builtin"
	"github.com/conduitio/conduit/pkg/plugin/connector/mock"
	"github.com/matryer/is"
	"go.uber.org/mock/gomock"
)

func TestClassicBatchDefaults(t *testing.T) {
	defaults := map[string]string{sdkBatchSizeKey: "1000", sdkBatchDelayKey: "100ms"}
	with := func(base, extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	tests := []struct {
		name    string
		builtin string
		in      map[string]string
		want    map[string]string // nil: unchanged
	}{
		{"postgres, nothing set", "postgres", map[string]string{"url": "u"}, with(map[string]string{"url": "u"}, defaults)},
		{"kafka, nothing set", "kafka", map[string]string{"servers": "s"}, with(map[string]string{"servers": "s"}, defaults)},
		{"kafka, mirror=false", "kafka", map[string]string{"mirror": "false"}, with(map[string]string{"mirror": "false"}, defaults)},
		{"kafka, empty mirror", "kafka", map[string]string{"mirror": " "}, with(map[string]string{"mirror": " "}, defaults)},

		{"not allowlisted: s3", "s3", map[string]string{}, nil},
		{"not allowlisted: file", "file", map[string]string{}, nil},
		{"not built-in (standalone with the same bare name)", "", map[string]string{}, nil},

		{"kafka, mirror=true", "kafka", map[string]string{"mirror": "true"}, nil},
		{"kafka, mirror=TRUE padded", "kafka", map[string]string{" mirror ": " TRUE "}, nil},
		{"kafka, mirror=1", "kafka", map[string]string{"mirror": "1"}, nil},
		{"kafka, mistyped mirror is left alone", "kafka", map[string]string{"mirror": "yes please"}, nil},
		{"postgres ignores a mirror key", "postgres", map[string]string{"mirror": "true"}, with(map[string]string{"mirror": "true"}, defaults)},

		// The two keys are one unit.
		{"size set", "postgres", map[string]string{"sdk.batch.size": "50"}, nil},
		{"delay set", "postgres", map[string]string{"sdk.batch.delay": "1s"}, nil},
		{"both set", "postgres", map[string]string{"sdk.batch.size": "50", "sdk.batch.delay": "1s"}, nil},
		{"opt-out: size 1", "postgres", map[string]string{"sdk.batch.size": "1"}, nil},
		{"opt-out: size 0", "kafka", map[string]string{"sdk.batch.size": "0"}, nil},
		{"explicit defaults are the same as set", "postgres", map[string]string{"sdk.batch.size": "1000", "sdk.batch.delay": "100ms"}, nil},
		{"padded key", "postgres", map[string]string{" sdk.batch.size": " 5 "}, nil},
		{"explicit 0/0", "postgres", map[string]string{"sdk.batch.size": "0", "sdk.batch.delay": "0"}, nil},
		// An empty value is "unset" for the SDK (ApplyDefaults), so it is for us.
		{"empty size counts as unset", "postgres", map[string]string{"sdk.batch.size": ""}, with(map[string]string{"sdk.batch.size": ""}, defaults)},
		// The key is case-sensitive in the SDK, so SDK.BATCH.SIZE is not the setting.
		{"wrong-case key is not the setting", "postgres", map[string]string{"SDK.BATCH.SIZE": "5"}, with(map[string]string{"SDK.BATCH.SIZE": "5"}, defaults)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			got := with(tc.in, nil)
			applied, _ := classicBatchDefaults(got, tc.builtin)
			if tc.want == nil {
				is.True(!applied)
				is.Equal(got, tc.in)
				return
			}
			is.True(applied)
			is.Equal(got, tc.want)
		})
	}
}

// namedDispenser makes a mock dispenser look like a built-in connector's.
type namedDispenser struct {
	*mock.Dispenser
	name string
}

func (d namedDispenser) BuiltinPluginName() (string, bool) { return d.name, d.name != "" }

var _ connectorPlugin.BuiltinDispenser = namedDispenser{}

// configureSettings opens a destination whose dispenser is the built-in
// connector named builtinName and returns the settings the plugin's Configure
// received.
func configureSettings(
	t *testing.T, builtinName string, classic bool, settings map[string]string,
) (got map[string]string, dest *Destination) {
	t.Helper()
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	dest, destinationMock := newTestDestination(ctx, t, ctrl)
	dest.dispenser = namedDispenser{Dispenser: dest.dispenser.(*mock.Dispenser), name: builtinName}
	dest.Instance.Config.Settings = settings
	dest.Instance.LastActiveConfig = dest.Instance.Config
	if classic {
		dest.SetClassicDefaultBatching()
	}

	destinationMock.EXPECT().Configure(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req pconnector.DestinationConfigureRequest) (pconnector.DestinationConfigureResponse, error) {
			got = map[string]string(req.Config)
			return pconnector.DestinationConfigureResponse{}, nil
		},
	)
	destinationMock.EXPECT().Open(gomock.Any(), pconnector.DestinationOpenRequest{}).Return(pconnector.DestinationOpenResponse{}, nil)
	stream := &builtin.InMemoryDestinationRunStream{}
	destinationMock.EXPECT().NewStream().Return(stream)
	destinationMock.EXPECT().Run(gomock.Any(), stream).DoAndReturn(func(ctx context.Context, _ pconnector.DestinationRunStream) error {
		stream.Init(ctx)
		return nil
	})

	is.NoErr(dest.Open(ctx))
	return got, dest
}

func TestDestination_ClassicDefaultBatchingInjectsIntoConfigureOnly(t *testing.T) {
	is := is.New(t)
	user := map[string]string{"url": "u"}
	got, dest := configureSettings(t, "postgres", true, user)

	is.Equal(got, map[string]string{"url": "u", "sdk.batch.size": "1000", "sdk.batch.delay": "100ms"})
	// The stored config, which is persisted and compared for lifecycle events,
	// is untouched.
	is.Equal(dest.Instance.Config.Settings, map[string]string{"url": "u"})
	is.Equal(dest.Instance.LastActiveConfig.Settings, map[string]string{"url": "u"})
}

func TestDestination_ClassicDefaultBatchingNotAppliedOutsideClassic(t *testing.T) {
	// The classic engine did not ask (arch-v2 and the DLQ never do): even an
	// allowlisted built-in gets nothing injected.
	is := is.New(t)
	got, _ := configureSettings(t, "postgres", false, map[string]string{"url": "u"})
	is.Equal(got, map[string]string{"url": "u"})
}

func TestDestination_ClassicDefaultBatchingNotAppliedToStandalone(t *testing.T) {
	is := is.New(t)
	got, _ := configureSettings(t, "", true, map[string]string{"url": "u"})
	is.Equal(got, map[string]string{"url": "u"})
}

func TestDestination_ClassicDefaultBatchingUserOptOut(t *testing.T) {
	is := is.New(t)
	got, _ := configureSettings(t, "kafka", true, map[string]string{"sdk.batch.size": "1"})
	is.Equal(got, map[string]string{"sdk.batch.size": "1"}) // no delay injected: a full opt-out on every SDK version
}

func TestDestination_ClassicDefaultBatchingKafkaMirrorExcluded(t *testing.T) {
	is := is.New(t)
	got, _ := configureSettings(t, "kafka", true, map[string]string{"mirror": "true"})
	is.Equal(got, map[string]string{"mirror": "true"})
}

func TestDestination_EngineBatchesWinsOverClassicDefault(t *testing.T) {
	// arch-v2 strips the user's keys; the classic default must not be put back.
	is := is.New(t)
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	dest, destinationMock := newTestDestination(ctx, t, ctrl)
	dest.dispenser = namedDispenser{Dispenser: dest.dispenser.(*mock.Dispenser), name: "postgres"}
	dest.Instance.Config.Settings = map[string]string{"sdk.batch.size": "100"}
	dest.Instance.LastActiveConfig = dest.Instance.Config
	dest.SetEngineBatches()
	dest.SetClassicDefaultBatching() // would be a bug elsewhere; engineBatches must still win

	destinationMock.EXPECT().Configure(gomock.Any(), pconnector.DestinationConfigureRequest{Config: map[string]string{}}).
		Return(pconnector.DestinationConfigureResponse{}, nil)
	destinationMock.EXPECT().Open(gomock.Any(), pconnector.DestinationOpenRequest{}).Return(pconnector.DestinationOpenResponse{}, nil)
	stream := &builtin.InMemoryDestinationRunStream{}
	destinationMock.EXPECT().NewStream().Return(stream)
	destinationMock.EXPECT().Run(gomock.Any(), stream).DoAndReturn(func(ctx context.Context, _ pconnector.DestinationRunStream) error {
		stream.Init(ctx)
		return nil
	})
	is.NoErr(dest.Open(ctx))
}
