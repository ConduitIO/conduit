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

package filterjs

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/filter"
	"github.com/rs/zerolog"
)

const (
	// todo maybe change to transformjs?
	filterName = "filterjs"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister(filterName, NewProcessorBuilder())
}

// NewProcessorBuilder is a utility function for creating a processor.Builder for transforms.
func NewProcessorBuilder() processor.Builder {
	return func(config processor.Config) (processor.Processor, error) {
		return BuildFilter(config.Settings)
	}
}

// todo docs
func BuildFilter(config Config) (filter.Filter, error) {
	if config.script() == "" {
		return nil, cerrors.Errorf("%s: missing script", filterName)
	}

	// TODO get logger from config or some other place
	logger := zerolog.New(zerolog.NewConsoleWriter()).With().Timestamp().Logger()
	f, err := NewFilter(config.script(), config.negate(), logger)
	if err != nil {
		return nil, cerrors.Errorf("%s: %w", filterName, err)
	}

	return f.Filter, nil
}
