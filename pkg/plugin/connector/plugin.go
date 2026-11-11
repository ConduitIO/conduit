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

import "github.com/conduitio/conduit-connector-protocol/pconnector"

// Dispenser dispenses specifier, source and destination plugins.
type Dispenser interface {
	DispenseSpecifier() (SpecifierPlugin, error)
	DispenseSource() (SourcePlugin, error)
	DispenseDestination() (DestinationPlugin, error)
}

type SourcePlugin interface {
	pconnector.SourcePlugin
	NewStream() pconnector.SourceRunStream
}

type DestinationPlugin interface {
	pconnector.DestinationPlugin
	NewStream() pconnector.DestinationRunStream
}

type SpecifierPlugin interface {
	pconnector.SpecifierPlugin
}

// specificationCarrier is implemented by a Dispenser that knows the
// specification of the plugin it dispenses, because the registry that built it
// had already loaded that specification when it resolved the plugin name.
type specificationCarrier interface {
	Specification() (pconnector.Specification, bool)
}

// DispenserSpecification returns the specification of the plugin d dispenses,
// and false if d does not carry one. The built-in and standalone registries
// attach the specification of the exact plugin version they resolved, so the
// specification and the plugin a later Dispense call starts cannot disagree,
// and reading it starts no plugin. Other dispensers (test doubles) carry none.
//
// The arch-v2 engine reads it to resolve a destination's effective
// sdk.batch.* settings (docs/design-documents/
// 20261103-archv2-destination-batch-timing.md, Decision 2).
func DispenserSpecification(d Dispenser) (pconnector.Specification, bool) {
	if c, ok := d.(specificationCarrier); ok {
		return c.Specification()
	}
	return pconnector.Specification{}, false
}
