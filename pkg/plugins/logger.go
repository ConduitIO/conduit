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

package plugins

import (
	"fmt"
	"io"
	stdlog "log"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/rs/zerolog"
)

var hclogZerologLevelMapping = map[hclog.Level]zerolog.Level{
	hclog.NoLevel: zerolog.NoLevel,
	hclog.Trace:   zerolog.TraceLevel,
	hclog.Debug:   zerolog.DebugLevel,
	hclog.Info:    zerolog.InfoLevel,
	hclog.Warn:    zerolog.WarnLevel,
	hclog.Error:   zerolog.ErrorLevel,
}

type hcLogger struct {
	name   string
	logger zerolog.Logger
}

var _ hclog.Logger = (*hcLogger)(nil)

func (h *hcLogger) Log(level hclog.Level, msg string, args ...interface{}) {
	zlevel := hclogZerologLevelMapping[level]
	h.applyArgs(h.logger.WithLevel(zlevel), args).Msg(msg)
}

func (h *hcLogger) Trace(msg string, args ...interface{}) {
	h.applyArgs(h.logger.Trace(), args).Msg(msg)
}

func (h *hcLogger) Debug(msg string, args ...interface{}) {
	h.applyArgs(h.logger.Debug(), args).Msg(msg)
}

func (h *hcLogger) Info(msg string, args ...interface{}) {
	h.applyArgs(h.logger.Info(), args).Msg(msg)
}

func (h *hcLogger) Warn(msg string, args ...interface{}) {
	h.applyArgs(h.logger.Warn(), args).Msg(msg)
}

func (h *hcLogger) Error(msg string, args ...interface{}) {
	h.applyArgs(h.logger.Error(), args).Msg(msg)
}

func (h *hcLogger) IsTrace() bool {
	return h.isLevel(zerolog.TraceLevel)
}

func (h *hcLogger) IsDebug() bool {
	return h.isLevel(zerolog.DebugLevel)
}

func (h *hcLogger) IsInfo() bool {
	return h.isLevel(zerolog.InfoLevel)
}

func (h *hcLogger) IsWarn() bool {
	return h.isLevel(zerolog.WarnLevel)
}

func (h *hcLogger) IsError() bool {
	return h.isLevel(zerolog.ErrorLevel)
}

func (h *hcLogger) ImpliedArgs() []interface{} {
	panic("implement me") // TODO
}

func (h *hcLogger) With(args ...interface{}) hclog.Logger {
	c := h.logger.With()
	for i := 0; i < len(args)-1; i += 2 {
		key := args[i].(string) // keys must be strings
		val := args[i+1]
		c.Interface(key, val)
	}
	h.logger = c.Logger()
	return h
}

func (h *hcLogger) Name() string {
	return h.name
}

func (h *hcLogger) Named(name string) hclog.Logger {
	hCopy := *h
	hCopy.name = strings.TrimLeft(h.name+" "+name, " ")
	return &hCopy
}

func (h *hcLogger) ResetNamed(name string) hclog.Logger {
	hCopy := *h
	hCopy.name = name
	return &hCopy
}

func (h *hcLogger) SetLevel(level hclog.Level) {
	zlevel := hclogZerologLevelMapping[level]
	h.logger = h.logger.Level(zlevel)
}

func (h *hcLogger) StandardLogger(opts *hclog.StandardLoggerOptions) *stdlog.Logger {
	var prefix string
	if h.name != "" {
		prefix = fmt.Sprintf("%s: ", h.name)
	}

	return stdlog.New(h.StandardWriter(opts), prefix, 0)
}

func (h *hcLogger) StandardWriter(opts *hclog.StandardLoggerOptions) io.Writer {
	return h.logger
}

func (h *hcLogger) applyArgs(e *zerolog.Event, args []interface{}) *zerolog.Event {
	if h.name != "" {
		// add component name
		e.Str("component", h.name)
	}
	for i := 0; i < len(args)-1; i += 2 {
		key := args[i].(string) // keys must be strings
		val := args[i+1]
		e.Interface(key, val)
	}
	return e
}

func (h *hcLogger) isLevel(lvl zerolog.Level) bool {
	return lvl >= h.logger.GetLevel() && lvl >= zerolog.GlobalLevel()
}
