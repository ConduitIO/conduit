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
	"reflect"
	"strings"
	"testing"

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
	// component is attached to all messages and can be replaced
	component string
}

// New creates a new CtxLogger with the supplied zerolog.Logger.
func New(logger zerolog.Logger) CtxLogger {
	return CtxLogger{Logger: logger}
}

// Nop returns a disabled logger for which all operation are no-op.
func Nop() CtxLogger {
	return CtxLogger{Logger: zerolog.Nop()}
}

// Test returns a test logger that writes to the supplied testing.TB.
func Test(t testing.TB) CtxLogger {
	return CtxLogger{Logger: zerolog.New(zerolog.NewTestWriter(t))}
}

// InitLogger returns a logger initialized with the wanted level and format
func InitLogger(level zerolog.Level, f Format) CtxLogger {
	w := GetWriter(f)
	logger := zerolog.New(w).
		With().
		Timestamp().
		Stack().
		Logger().
		Level(level)

	return New(logger)
}

// WithComponent adds the component to the output. This function can be called
// multiple times with the same value and it will produce the same result. If
// component is an empty string then nothing will be added to the output.
func (l CtxLogger) WithComponent(component string) CtxLogger {
	l.component = component
	return l
}

func (l CtxLogger) WithComponentFromType(c any) CtxLogger {
	cType := reflect.TypeOf(c)
	for cType.Kind() == reflect.Ptr || cType.Kind() == reflect.Interface {
		cType = cType.Elem()
	}

	pkgPath := cType.PkgPath()
	pkgPath = strings.TrimPrefix(pkgPath, "github.com/conduitio/conduit/pkg/")
	pkgPath = strings.ReplaceAll(pkgPath, "/", ".")
	typeName := cType.Name()
	l.component = pkgPath + "." + typeName
	return l
}

func (l CtxLogger) Component() string {
	return l.component
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
	return l.attachComponent(l.Logger.Trace().Ctx(ctx))
}

// Debug starts a new message with debug level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Debug(ctx context.Context) *zerolog.Event {
	return l.attachComponent(l.Logger.Debug().Ctx(ctx))
}

// Info starts a new message with info level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Info(ctx context.Context) *zerolog.Event {
	return l.attachComponent(l.Logger.Info().Ctx(ctx))
}

// Warn starts a new message with warn level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Warn(ctx context.Context) *zerolog.Event {
	return l.attachComponent(l.Logger.Warn().Ctx(ctx))
}

// Error starts a new message with error level and context ctx.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Error(ctx context.Context) *zerolog.Event {
	return l.attachComponent(l.Logger.Error().Ctx(ctx))
}

// Err starts a new message with context ctx and error level with err as a field
// if not nil or with info level if err is nil.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Err(ctx context.Context, err error) *zerolog.Event {
	return l.attachComponent(l.Logger.Err(err).Ctx(ctx))
}

// Fatal starts a new message with fatal level and context ctx. The os.Exit(1)
// function is called by the Msg method, which terminates the program
// immediately.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Fatal(ctx context.Context) *zerolog.Event {
	return l.attachComponent(l.Logger.Fatal().Ctx(ctx))
}

// Panic starts a new message with panic level and context ctx. The panic()
// function is called by the Msg method, which stops the ordinary flow of a
// goroutine.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Panic(ctx context.Context) *zerolog.Event {
	return l.attachComponent(l.Logger.Panic().Ctx(ctx))
}

// WithLevel starts a new message with level and context ctx. Unlike Fatal and
// Panic methods, WithLevel does not terminate the program or stop the ordinary
// flow of a gourotine when used with their respective levels.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) WithLevel(ctx context.Context, level zerolog.Level) *zerolog.Event {
	return l.attachComponent(l.Logger.WithLevel(level).Ctx(ctx))
}

// Log starts a new message with no level and context ctx. Setting GlobalLevel
// to Disabled will still disable events produced by this method.
//
// You must call Msg on the returned event in order to send the event.
func (l CtxLogger) Log(ctx context.Context) *zerolog.Event {
	return l.attachComponent(l.Logger.Log().Ctx(ctx))
}

func (l CtxLogger) attachComponent(e *zerolog.Event) *zerolog.Event {
	if l.component != "" {
		e.Str(ComponentField, l.component)
	}
	return e
}
