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

package builtinv1

import (
	"testing"

	schemamock "github.com/conduitio/conduit-connector-protocol/conduit/schema/mock"
	"github.com/conduitio/conduit-connector-protocol/cpluginv1"
	"github.com/conduitio/conduit-connector-protocol/cpluginv1/mock"
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
	mockSchemaService := schemamock.NewService(ctrl)
	
	dispenser := NewDispenser(
		"mockPlugin",
		logger,
		mockSchemaService,
		func() cpluginv1.SpecifierPlugin { return mockSpecifier },
		func() cpluginv1.SourcePlugin { return mockSource },
		func() cpluginv1.DestinationPlugin { return mockDestination },
	)
	return dispenser, mockSpecifier, mockSource, mockDestination
}
