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

package fromplugin

import (
	"github.com/conduitio/conduit-connector-protocol/cpluginv2"
	connectorPlugin "github.com/conduitio/conduit/pkg/plugin/connector"
)

func SpecifierSpecifyResponse(in cpluginv2.SpecifierSpecifyResponse) (connectorPlugin.Specification, error) {
	return connectorPlugin.Specification{
		Name:              in.Name,
		Summary:           in.Summary,
		Description:       in.Description,
		Version:           in.Version,
		Author:            in.Author,
		DestinationParams: in.DestinationParams,
		SourceParams:      in.SourceParams,
	}, nil
}
