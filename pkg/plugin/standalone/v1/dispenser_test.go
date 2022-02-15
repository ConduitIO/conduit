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

package standalonev1

import (
	"context"
	"testing"
	"time"

	"github.com/conduitio/connector-plugin/cpluginv1"
	"github.com/conduitio/connector-plugin/cpluginv1/mock"
	"github.com/conduitio/connector-plugin/cpluginv1/server"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/golang/mock/gomock"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/rs/zerolog"
)

func newTestDispenser(t *testing.T, logger zerolog.Logger) (
	plugin.Dispenser,
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
		err := server.Serve(
			func() cpluginv1.SpecifierPlugin { return mockSpecifier },
			func() cpluginv1.SourcePlugin { return mockSource },
			func() cpluginv1.DestinationPlugin { return mockDestination },
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
	dispenser, err := NewDispenser(logger, "", WithReattachConfig(config))
	if err != nil {
		t.Fatal("could not create dispenser", err)
	}

	return dispenser, mockSpecifier, mockSource, mockDestination
}
