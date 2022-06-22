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

package txfjs

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/transform"
	"github.com/rs/zerolog"
)

const (
	transformName = "js"
	configScript  = "script"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(transformName, transform.NewBuilder(Builder))
}

// Builder parses the config and if valid returns a JS transform, an error
// otherwise. It requires the config field "script".
func Builder(config transform.Config) (transform.Transform, error) {
	if config[configScript] == "" {
		return nil, cerrors.Errorf("%s: unspecified field %q", transformName, configScript)
	}

	// TODO get logger from config or some other place
	logger := zerolog.New(zerolog.NewConsoleWriter()).With().Timestamp().Logger()
	t, err := NewTransformer(config[configScript], logger)
	if err != nil {
		return nil, cerrors.Errorf("%s: %w", transformName, err)
	}

	return t.Transform, nil
}
