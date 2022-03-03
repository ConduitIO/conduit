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

//go:generate mockgen -destination=mock/iterator.go -package=mock . Iterator

package source

import (
	"context"

	"github.com/conduitio/conduit/pkg/plugin/sdk"
)

// Strategy defines an iterator interface that all Iterators must fulfill.
// It iterates over a first in first out queue.
type Strategy interface {
	// Next pops off the next record in the queue or an error.
	Next(ctx context.Context) (sdk.Record, error)
	// Teardown attempts to gracefully teardown the queue.
	Teardown() error
}
