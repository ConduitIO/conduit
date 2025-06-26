// Copyright © 2024 Meroxa, Inc.
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

package common

import (
	"io"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/rs/zerolog"
	"github.com/tetratelabs/wazero"
)

// WasmLogWriter is a logger adapter for the WASM stderr and stdout streams.
// It parses the JSON log events and emits them as structured logs. It expects
// the log events to be in the default format produced by zerolog. If the
// parsing fails, it falls back to writing the raw bytes as-is.
type WasmLogWriter struct {
	logger zerolog.Logger
}

var _ io.Writer = (*WasmLogWriter)(nil)

func NewWasmLogWriter(logger log.CtxLogger, module wazero.CompiledModule) WasmLogWriter {
	name := module.Name()
	if name == "" {
		// no module name, use the component name instead
		name = logger.Component() + ".module"
	}
	logger = logger.WithComponent(name)
	return WasmLogWriter{logger: logger.ZerologWithComponent()}
}

func (l WasmLogWriter) Write(p []byte) (int, error) {
	err := l.emitJSONEvent(p)
	if err != nil {
		// fallback to writing the bytes as-is
		return l.logger.Write(p)
	}
	return len(p), nil
}

func (l WasmLogWriter) emitJSONEvent(p []byte) error {
	var raw map[string]any
	err := json.Unmarshal(p, &raw)
	if err != nil {
		return err
	}

	var (
		level = zerolog.DebugLevel // default
		msg   = ""
	)

	// parse level
	if v, ok := raw[zerolog.LevelFieldName]; ok {
		delete(raw, zerolog.LevelFieldName)
		if s, ok := v.(string); ok {
			parsedLvl, err := zerolog.ParseLevel(s)
			if err == nil {
				level = parsedLvl
			}
		}
	}

	// prepare log event
	e := l.logger.WithLevel(level)
	if !e.Enabled() {
		return nil
	}

	// parse message
	if v, ok := raw[zerolog.MessageFieldName]; ok {
		delete(raw, zerolog.MessageFieldName)
		if s, ok := v.(string); ok {
			msg = s
		}
	}

	// don't duplicate timestamp, it's added by zerolog
	delete(raw, zerolog.TimestampFieldName)

	// parse unknown fields
	for k, v := range raw {
		e.Any(k, v)
	}

	e.Msg(msg)
	return nil
}
