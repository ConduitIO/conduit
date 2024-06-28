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

package standalone

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit-connector-protocol/pconnector"
	"github.com/conduitio/conduit-connector-protocol/pconnector/client"
	"github.com/conduitio/conduit-connector-protocol/pconnector/mock"
	"github.com/conduitio/conduit-connector-protocol/pconnector/server"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

func newTestDispenser(t *testing.T, logger zerolog.Logger, version int) (
	connector.Dispenser,
	*mock.SpecifierPlugin,
	*mock.SourcePlugin,
	*mock.DestinationPlugin,
) {
	ctx, cancel := context.WithCancel(context.Background())
	ctrl := gomock.NewController(t)

	ch := make(chan *goplugin.ReattachConfig, 1)
	closeCh := make(chan struct{})

	mockSpecifier := mock.NewSpecifierPlugin(ctrl)
	mockSource := mock.NewSourcePlugin(ctrl)
	mockDestination := mock.NewDestinationPlugin(ctrl)

	t.Cleanup(func() {
		cancel()
		<-closeCh // wait for plugin to stop
	})
	go func() {
		defer close(closeCh)
		// Trick to convince the plugin it should use a specific protocol version
		os.Setenv("PLUGIN_PROTOCOL_VERSIONS", strconv.Itoa(version))
		err := server.Serve(
			func() pconnector.SpecifierPlugin { return mockSpecifier },
			func() pconnector.SourcePlugin { return mockSource },
			func() pconnector.DestinationPlugin { return mockDestination },
			server.WithDebug(ctx, ch, nil),
		)
		if err != nil {
			t.Logf("error serving plugin: %+v", err)
		}
	}()

	// We should get a config
	var config *goplugin.ReattachConfig
	select {
	case config = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("should've received reattach")
	}
	if config == nil {
		t.Fatal("config should not be nil")
	}

	// Connect
	dispenser, err := NewDispenser(logger, "", client.WithReattachConfig(config), reattachVersionedPluginOption{version: version})
	if err != nil {
		t.Fatal("could not create dispenser", err)
	}

	return dispenser, mockSpecifier, mockSource, mockDestination
}

// reattachVersionedPluginOption is a client.Option that sets ClientConfig.Plugins
// to the correct versioned plugins for the given version. It's used because
// version negotiation is skipped when reattaching.
//
// This is a bandage on a bug found in go-plugin v1.6.1.
// GitHub issue: https://github.com/hashicorp/go-plugin/issues/310
type reattachVersionedPluginOption struct {
	version int
}

func (r reattachVersionedPluginOption) ApplyOption(cfg *goplugin.ClientConfig) error {
	cfg.Plugins = cfg.VersionedPlugins[r.version]
	return nil
}
