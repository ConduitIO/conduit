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

package stream

import (
	"reflect"

	"github.com/conduitio/conduit/pkg/foundation/log"
)

// SetLogger figures out if the node needs a logger, sets static metadata in the
// logger and supplies it to the node.
func SetLogger(n Node, logger log.CtxLogger) {
	ln, ok := n.(LoggingNode)
	if !ok {
		return
	}

	nt := reflect.TypeOf(ln)
	for nt.Kind() == reflect.Ptr {
		nt = nt.Elem()
	}

	logger = logger.WithComponent(nt.Name())
	logger.Logger = logger.With().Str(log.NodeIDField, n.ID()).Logger()

	ln.SetLogger(logger)
}
