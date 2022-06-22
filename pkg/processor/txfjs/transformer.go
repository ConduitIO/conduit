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
	"fmt"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/processor"
	"github.com/conduitio/conduit/pkg/processor/javascript"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/rs/zerolog"
)

const (
	entrypoint = "transform"
)

// Transformer is able to run transformations defined in JavaScript.
type Transformer struct {
	jsFunc javascript.Function
}

func NewTransformer(src string, logger zerolog.Logger) (*Transformer, error) {
	jsFunc, err := javascript.NewFunction(src, entrypoint, logger)
	if err != nil {
		return nil, fmt.Errorf("failed creating JavaScript function: %w", err)
	}

	return &Transformer{jsFunc: jsFunc}, nil
}

func (t *Transformer) Transform(in record.Record) (record.Record, error) {
	out, err := t.jsFunc.Call(in)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed to transform to JS record: %w", err)
	}

	if out == nil {
		return record.Record{}, processor.ErrSkipRecord
	}

	return *out, nil
}
