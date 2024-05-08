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

package fromproto

import (
	"github.com/conduitio/conduit/pkg/connector"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
)

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	var cTypes [1]struct{}
	_ = cTypes[int(connector.TypeSource)-int(apiv1.Connector_TYPE_SOURCE)]
	_ = cTypes[int(connector.TypeDestination)-int(apiv1.Connector_TYPE_DESTINATION)]
}

func ConnectorType(in apiv1.Connector_Type) connector.Type {
	return connector.Type(in)
}

func ConnectorConfig(in *apiv1.Connector_Config) connector.Config {
	if in == nil {
		return connector.Config{}
	}

	settings := in.Settings

	if settings == nil {
		settings = make(map[string]string)
	}

	return connector.Config{
		Name:     in.Name,
		Settings: settings,
	}
}
