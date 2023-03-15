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
// logger and supplies it to the node. Behavior can be customized by supplying
// custom options.
func SetLogger(n Node, logger log.CtxLogger, options ...func(log.CtxLogger, Node) log.CtxLogger) {
	ln, ok := n.(LoggingNode)
	if !ok {
		return
	}

	if len(options) == 0 {
		// default options
		logger = LoggerWithNodeID(logger, n)
		logger = LoggerWithComponent(logger, n)
	} else {
		for _, c := range options {
			logger = c(logger, n)
		}
	}
	ln.SetLogger(logger)
}

// LoggerWithNodeID creates a logger with the node ID field.
func LoggerWithNodeID(logger log.CtxLogger, n Node) log.CtxLogger {
	logger.Logger = logger.With().Str(log.NodeIDField, n.ID()).Logger()
	return logger
}

// LoggerWithComponent creates a logger with the component set to the node name.
func LoggerWithComponent(logger log.CtxLogger, v Node) log.CtxLogger {
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	logger = logger.WithComponent(t.Name())
	return logger
}
