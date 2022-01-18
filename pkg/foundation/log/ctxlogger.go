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

package log

import (
	"context"
	"os"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/rs/zerolog"
)

func init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	zerolog.ErrorStackMarshaler = cerrors.GetStackTrace
}

// CtxLogger is a wrapper around a zerolog.Logger which adds support for adding
// context hooks to it. All methods that return *zerolog.Event are switched for
// versions that take a context and trigger context hooks before returning the
// entry.
type CtxLogger struct {
	zerolog.Logger
	hooks []CtxHook
	// component hook is saved separately from other hooks, so we can replace it
	// when requested
	ch componentHook
}

// New creates a new CtxLogger with the supplied zerolog.Logger.
func New(logger zerolog.Logger) CtxLogger {
	return CtxLogger{Logger: logger}
}

// Nop returns a disabled logger for which all operation are no-op.
func Nop() CtxLogger {
	return CtxLogger{Logger: zerolog.Nop()}
}

// Prod returns a production logger. Output is formatted as JSON, minimum level
// is set to INFO.
func Prod() CtxLogger {
	zlogger := zerolog.New(os.Stderr).
		With().
		Timestamp().
		Stack().
		Logger().
		Level(zerolog.InfoLevel)

	return New(zlogger)
}

// Dev returns a development logger. Output is human readable, minimum level is
// set to DEBUG.
func Dev() CtxLogger {
	w := zerolog.NewConsoleWriter()
	w.TimeFormat = time.StampMilli

	zlogger := zerolog.New(w).
		With().
		Timestamp().
		Stack().
		Logger().
		Level(zerolog.DebugLevel)

	return New(zlogger)
}

// CtxHook defines an interface for a log context hook.
type CtxHook interface {
	// Run runs the hook with the logger context.
	Run(ctx context.Context, e *zerolog.Event, l zerolog.Level)
}

// CtxHookFunc is an adaptor to allow the use of an ordinary function as a Hook.
type CtxHookFunc func(ctx context.Context, e *zerolog.Event, l zerolog.Level)

// Run implements the CtxHook interface.
func (h CtxHookFunc) Run(ctx context.Context, e *zerolog.Event, l zerolog.Level) {
	h(ctx, e, l)
}

// CtxHook returns a logger with the h CtxHook.
func (l CtxLogger) CtxHook(h ...CtxHook) CtxLogger {
	l.hooks = append(l.hooks, h...)
	return l
}

// WithComponent adds the component to the output. This function can be called
// multiple times with the same value and it will produce the same result. If
// component is an empty string then nothing will be added to the output.
func (l CtxLogger) WithComponent(component string) CtxLogger {
	l.ch.name = component
	return l
}

func (l CtxLogger) Component() string {
	return l.ch.name
}

// ZerologWithComponent returns a zerolog.Logger that adds the component field
// to all log entries.
func (l CtxLogger) ZerologWithComponent() zerolog.Logger {
	out := l.Logger
	if l.Component() != "" {
		out = out.With().Str(ComponentField, l.Component()).Logger()
	}
	return out
}

// Trace starts a new message with trace level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Trace(ctx context.Context) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.Trace(), zerolog.TraceLevel)
}

// Debug starts a new message with debug level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Debug(ctx context.Context) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.Debug(), zerolog.DebugLevel)
}

// Info starts a new message with info level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Info(ctx context.Context) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.Info(), zerolog.InfoLevel)
}

// Warn starts a new message with warn level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Warn(ctx context.Context) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.Warn(), zerolog.WarnLevel)
}

// Error starts a new message with error level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Error(ctx context.Context) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.Error(), zerolog.ErrorLevel)
}

// Err starts a new message with context ctx and error level with err as a field
// if not nil or with info level if err is nil.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Err(ctx context.Context, err error) *zerolog.Event {
	if err != nil {
		return l.Error(ctx).Err(err)
	}
	return l.Info(ctx)
}

// Fatal starts a new message with fatal level and context ctx. The os.Exit(1)
// function is called by the Msg method, which terminates the program
// immediately.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Fatal(ctx context.Context) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.Fatal(), zerolog.FatalLevel)
}

// Panic starts a new message with panic level and context ctx. The panic()
// function is called by the Msg method, which stops the ordinary flow of a
// goroutine.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Panic(ctx context.Context) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.Panic(), zerolog.PanicLevel)
}

// WithLevel starts a new message with level and context ctx. Unlike Fatal and
// Panic methods, WithLevel does not terminate the program or stop the ordinary
// flow of a gourotine when used with their respective levels.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) WithLevel(ctx context.Context, level zerolog.Level) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.WithLevel(level), level)
}

// Log starts a new message with no level and context ctx. Setting GlobalLevel
// to Disabled will still disable events produced by this method.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Log(ctx context.Context) *zerolog.Event {
	return l.runCtxHooks(ctx, l.Logger.Log(), zerolog.NoLevel)
}

// runCtxHooks runs all context hooks on the supplied event. If the event is
// disabled the hooks are skipped.
func (l CtxLogger) runCtxHooks(ctx context.Context, e *zerolog.Event, level zerolog.Level) *zerolog.Event {
	if e == nil || !e.Enabled() {
		return e
	}

	l.ch.Run(ctx, e, level)
	for _, h := range l.hooks {
		h.Run(ctx, e, level)
	}
	return e
}
