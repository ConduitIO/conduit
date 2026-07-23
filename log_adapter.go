// Copyright © 2026 Meroxa, Inc.
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

package conduit

import (
	"context"
	"log/slog"

	json "github.com/goccy/go-json"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/rs/zerolog"
)

// zerolog's own default field names (set by the zerolog package itself, never
// overridden anywhere in this module — see pkg/foundation/log/ctxlogger.go's
// init, which only touches TimeFieldFormat/ErrorStackMarshaler).
const (
	zerologFieldLevel   = "level"
	zerologFieldMessage = "message"
	zerologFieldTime    = "time"
)

// newSlogCtxLogger builds a log.CtxLogger that forwards every log line to
// logger instead of any Conduit-owned stdout writer. It is the internal
// zerolog->slog adapter alternative #2 in the embed workstream plan chose to
// keep in this package (unexported), rather than teaching
// pkg/foundation/log about slog: the dependency direction stays one-way
// (this package -> pkg/foundation/log, never the reverse).
//
// Conduit's logging is built on zerolog throughout (log.CtxLogger wraps
// zerolog.Logger); rather than a second, parallel logging implementation for
// the embed path, this constructs a real zerolog.Logger whose io.Writer
// decodes each JSON-encoded log line and re-emits it through logger via
// slog.Logger.LogAttrs, preserving level, message, and every structured
// field as an slog attribute. It never writes to os.Stdout/os.Stderr.
func newSlogCtxLogger(logger *slog.Logger) log.CtxLogger {
	zl := zerolog.New(&slogWriter{logger: logger}).With().Timestamp().Stack().Logger()
	return log.New(zl)
}

// slogWriter is an io.Writer that decodes each zerolog JSON log line (one
// Write call per line, as zerolog always emits) and re-emits it through an
// *slog.Logger. It is the single sink for Conduit's log output on the embed
// path — see newSlogCtxLogger.
type slogWriter struct {
	logger *slog.Logger
}

// Write implements io.Writer. It always reports success for the full byte
// count: zerolog does not retry or otherwise interpret a Write error, so a
// decode failure here is logged as a best-effort fallback message instead of
// being surfaced as a write error that zerolog has no useful way to react to.
func (w *slogWriter) Write(p []byte) (int, error) {
	n := len(p)

	var fields map[string]any
	if err := json.Unmarshal(p, &fields); err != nil {
		// Not a decodable JSON line (should not happen with zerolog's own
		// encoder) — surface the raw text rather than silently dropping it.
		w.logger.Info(string(p))
		return n, nil
	}

	level := slogLevel(fields[zerologFieldLevel])
	msg, _ := fields[zerologFieldMessage].(string)
	delete(fields, zerologFieldLevel)
	delete(fields, zerologFieldMessage)
	delete(fields, zerologFieldTime)

	attrs := make([]slog.Attr, 0, len(fields))
	for k, v := range fields {
		attrs = append(attrs, slog.Any(k, v))
	}

	// context.Background(): the originating log.CtxLogger call's ctx is not
	// available here (io.Writer.Write only receives encoded bytes) — slog
	// handlers in the standard library do not read values from ctx beyond
	// cancellation, so this is not a loss of information any handler Conduit
	// ships with would otherwise use.
	w.logger.LogAttrs(context.Background(), level, msg, attrs...)
	return n, nil
}

// slogLevel maps a zerolog level string to the nearest slog.Level. slog has
// no Trace/Fatal/Panic levels, so trace folds into Debug and fatal/panic fold
// into Error (Conduit's embed path never calls the CtxLogger methods that
// call os.Exit/panic on the host's behalf — see log.CtxLogger.Fatal/Panic's
// own doc — so this mapping is defensive, not expected to observe those
// levels in practice).
func slogLevel(v any) slog.Level {
	s, _ := v.(string)
	switch s {
	case "trace", "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error", "fatal", "panic":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
