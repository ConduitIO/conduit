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
	"testing"

	"github.com/conduitio/conduit-connector-protocol/pconnector/mock"
	v1 "github.com/conduitio/conduit-connector-protocol/pconnector/v1" //nolint:staticcheck
	v2 "github.com/conduitio/conduit-connector-protocol/pconnector/v2"
	"github.com/conduitio/conduit/pkg/plugin/connector"
	"github.com/rs/zerolog"
)

func TestAcceptanceV1(t *testing.T) {
	logger := zerolog.Nop()
	connector.AcceptanceTest(t, func(t *testing.T) (connector.Dispenser, *mock.SpecifierPlugin, *mock.SourcePlugin, *mock.DestinationPlugin) {
		return newTestDispenser(t, logger, v1.Version)
	})
}

func TestAcceptanceV2(t *testing.T) {
	logger := zerolog.Nop()
	connector.AcceptanceTest(t, func(t *testing.T) (connector.Dispenser, *mock.SpecifierPlugin, *mock.SourcePlugin, *mock.DestinationPlugin) {
		return newTestDispenser(t, logger, v2.Version)
	})
}
