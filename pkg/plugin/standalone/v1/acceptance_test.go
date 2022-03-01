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
	"testing"

	"github.com/conduitio/conduit-plugin-protocol/cpluginv1/mock"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/rs/zerolog"
)

func TestAcceptance(t *testing.T) {
	logger := zerolog.Nop()
	plugin.AcceptanceTestV1(t, func(t *testing.T) (plugin.Dispenser, *mock.SpecifierPlugin, *mock.SourcePlugin, *mock.DestinationPlugin) {
		return newTestDispenser(t, logger)
	})
}
