// Copyright © 2023 Meroxa, Inc.
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

//go:generate mockgen -typed -destination=mock/plugin.go -package=mock -mock_names=Dispenser=Dispenser,SourcePlugin=SourcePlugin,DestinationPlugin=DestinationPlugin,SpecifierPlugin=SpecifierPlugin . Dispenser,DestinationPlugin,SourcePlugin,SpecifierPlugin

package connector

import (
	"github.com/conduitio/conduit-connector-protocol/cplugin"
)

// Dispenser dispenses specifier, source and destination plugins.
type Dispenser interface {
	DispenseSpecifier() (SpecifierPlugin, error)
	DispenseSource() (SourcePlugin, error)
	DispenseDestination() (DestinationPlugin, error)
}

type SourcePlugin interface {
	cplugin.SourcePlugin
	NewStream() cplugin.SourceRunStream
}

type DestinationPlugin interface {
	cplugin.DestinationPlugin
	NewStream() cplugin.DestinationRunStream
}

type SpecifierPlugin interface {
	cplugin.SpecifierPlugin
}
