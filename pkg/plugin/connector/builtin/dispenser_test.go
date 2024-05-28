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

package builtin

import (
	"testing"

	"github.com/conduitio/conduit-connector-protocol/cplugin"
	"github.com/conduitio/conduit-connector-protocol/cplugin/mock"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/rs/zerolog"
	"go.uber.org/mock/gomock"
)

func newTestDispenser(t *testing.T) (connector.Dispenser, *mock.SpecifierPlugin, *mock.SourcePlugin, *mock.DestinationPlugin) {
	logger := log.InitLogger(zerolog.InfoLevel, log.FormatCLI)
	ctrl := gomock.NewController(t)

	mockSpecifier := mock.NewSpecifierPlugin(ctrl)
	mockSource := mock.NewSourcePlugin(ctrl)
	mockDestination := mock.NewDestinationPlugin(ctrl)

	dispenser := NewDispenser(
		"mockPlugin",
		logger,
		func() cplugin.SpecifierPlugin { return mockSpecifier },
		func() cplugin.SourcePlugin { return mockSource },
		func() cplugin.DestinationPlugin { return mockDestination },
	)
	return dispenser, mockSpecifier, mockSource, mockDestination
}
