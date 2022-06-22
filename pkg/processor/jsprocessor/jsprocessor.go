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

package jsprocessor

import (
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/multierror"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/filter/filterjs"
	"github.com/conduitio/conduit/pkg/processor/transform/txfjs"
)

func init() {
	processor.GlobalBuilderRegistry.MustRegister("js", NewJSProcessorBuilder)
}

func NewJSProcessorBuilder(config processor.Config) (processor.Processor, error) {
	f, fErr := filterjs.BuildFilter(config.Settings)
	t, tErr := txfjs.Builder(config.Settings)
	if fErr == nil || tErr == nil {
		return nil, cerrors.New("js script contains two entrypoint functions (transform, filter)")
	}
	if fErr != nil && tErr != nil {
		return nil, multierror.Append(fErr, tErr)
	}
	if fErr == nil {
		return f, nil
	}
	return t, nil
}
