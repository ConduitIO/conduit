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
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/rs/zerolog"
)

func TestCtxLoggerWithoutHooks(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name    string
		logfunc func(CtxLogger)
		want    string
	}{{
		name: "log empty",
		logfunc: func(logger CtxLogger) {
			logger.Log(ctx).Msg("")
		},
		want: `{}` + "\n",
	}, {
		name: "log one-field",
		logfunc: func(logger CtxLogger) {
			logger.Log(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"foo":"bar"}` + "\n",
	}, {
		name: "log two-field",
		logfunc: func(logger CtxLogger) {
			logger.Log(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"foo":"bar","n":123}` + "\n",
	}, {
		name: "trace empty",
		logfunc: func(logger CtxLogger) {
			logger.Trace(ctx).Msg("")
		},
		want: `{"level":"trace"}` + "\n",
	}, {
		name: "trace one-field",
		logfunc: func(logger CtxLogger) {
			logger.Trace(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"trace","foo":"bar"}` + "\n",
	}, {
		name: "trace two-field",
		logfunc: func(logger CtxLogger) {
			logger.Trace(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"trace","foo":"bar","n":123}` + "\n",
	}, {
		name: "debug empty",
		logfunc: func(logger CtxLogger) {
			logger.Debug(ctx).Msg("")
		},
		want: `{"level":"debug"}` + "\n",
	}, {
		name: "debug one-field",
		logfunc: func(logger CtxLogger) {
			logger.Debug(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"debug","foo":"bar"}` + "\n",
	}, {
		name: "debug two-field",
		logfunc: func(logger CtxLogger) {
			logger.Debug(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"debug","foo":"bar","n":123}` + "\n",
	}, {
		name: "info empty",
		logfunc: func(logger CtxLogger) {
			logger.Info(ctx).Msg("")
		},
		want: `{"level":"info"}` + "\n",
	}, {
		name: "info one-field",
		logfunc: func(logger CtxLogger) {
			logger.Info(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"info","foo":"bar"}` + "\n",
	}, {
		name: "info two-field",
		logfunc: func(logger CtxLogger) {
			logger.Info(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"info","foo":"bar","n":123}` + "\n",
	}, {
		name: "warn empty",
		logfunc: func(logger CtxLogger) {
			logger.Warn(ctx).Msg("")
		},
		want: `{"level":"warn"}` + "\n",
	}, {
		name: "warn one-field",
		logfunc: func(logger CtxLogger) {
			logger.Warn(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"warn","foo":"bar"}` + "\n",
	}, {
		name: "warn two-field",
		logfunc: func(logger CtxLogger) {
			logger.Warn(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"warn","foo":"bar","n":123}` + "\n",
	}, {
		name: "error empty",
		logfunc: func(logger CtxLogger) {
			logger.Error(ctx).Msg("")
		},
		want: `{"level":"error"}` + "\n",
	}, {
		name: "error one-field",
		logfunc: func(logger CtxLogger) {
			logger.Error(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"error","foo":"bar"}` + "\n",
	}, {
		name: "error two-field",
		logfunc: func(logger CtxLogger) {
			logger.Error(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"error","foo":"bar","n":123}` + "\n",
	}, {
		name: "err empty with error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, cerrors.New("foo")).Msg("")
		},
		want: `{"level":"error","stack":\[{"func":"github.com/conduitio/conduit/pkg/foundation/log.TestCtxLoggerWithoutHooks.func\d*","file":".*/conduit/pkg/foundation/log/ctxlogger_test.go","line":\d*}\],"error":"foo"}`,
	}, {
		name: "err one-field with error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, cerrors.New("foo")).Str("foo", "bar").Msg("")
		},
		want: `{"level":"error","stack":\[{"func":"github.com/conduitio/conduit/pkg/foundation/log.TestCtxLoggerWithoutHooks.func\d*","file":".*/conduit/pkg/foundation/log/ctxlogger_test.go","line":\d*}\],"error":"foo","foo":"bar"}\n`,
	}, {
		name: "err two-field with error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, cerrors.New("foo")).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"error","stack":\[{"func":"github.com/conduitio/conduit/pkg/foundation/log.TestCtxLoggerWithoutHooks.func\d*","file":".*/conduit/pkg/foundation/log/ctxlogger_test.go","line":\d*}\],"error":"foo","foo":"bar","n":123}\n`,
	}, {
		name: "err empty without error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, nil).Msg("")
		},
		want: `{"level":"info"}` + "\n",
	}, {
		name: "err one-field without error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, nil).Str("foo", "bar").Msg("")
		},
		want: `{"level":"info","foo":"bar"}` + "\n",
	}, {
		name: "err two-field without error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, nil).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"info","foo":"bar","n":123}` + "\n",
	}, {
		name: "with level empty",
		logfunc: func(logger CtxLogger) {
			logger.WithLevel(ctx, zerolog.DebugLevel).Msg("")
		},
		want: `{"level":"debug"}` + "\n",
	}, {
		name: "with level one-field",
		logfunc: func(logger CtxLogger) {
			logger.WithLevel(ctx, zerolog.InfoLevel).Str("foo", "bar").Msg("")
		},
		want: `{"level":"info","foo":"bar"}` + "\n",
	}, {
		name: "with level two-field",
		logfunc: func(logger CtxLogger) {
			logger.WithLevel(ctx, zerolog.WarnLevel).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"warn","foo":"bar","n":123}` + "\n",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			logger := New(zerolog.New(&out).With().Stack().Logger())
			tc.logfunc(logger)
			got := out.String()
			matched, err := regexp.Match(tc.want, []byte(got))
			if !matched || err != nil {
				t.Errorf("invalid log output:\ngot:  %v\nwant: %v", got, tc.want)
			}
		})
	}
}

func TestCtxLoggerWithHooks(t *testing.T) {
	type strVal struct{}
	type intVal struct{}
	ctx := context.Background()
	ctx = context.WithValue(ctx, strVal{}, "bar")
	ctx = context.WithValue(ctx, intVal{}, 123)

	strValCtxHook := func(ctx context.Context, e *zerolog.Event, l zerolog.Level) {
		e.Interface("strval", ctx.Value(strVal{}))
	}
	intValCtxHook := func(ctx context.Context, e *zerolog.Event, l zerolog.Level) {
		e.Interface("intval", ctx.Value(intVal{}))
	}

	testCases := []struct {
		name    string
		logfunc func(CtxLogger)
		want    string
	}{{
		name: "log empty",
		logfunc: func(logger CtxLogger) {
			logger.Log(ctx).Msg("")
		},
		want: `{"strval":"bar","intval":123}` + "\n",
	}, {
		name: "log one-field",
		logfunc: func(logger CtxLogger) {
			logger.Log(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"strval":"bar","intval":123,"foo":"bar"}` + "\n",
	}, {
		name: "log two-field",
		logfunc: func(logger CtxLogger) {
			logger.Log(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"strval":"bar","intval":123,"foo":"bar","n":123}` + "\n",
	}, {
		name: "trace empty",
		logfunc: func(logger CtxLogger) {
			logger.Trace(ctx).Msg("")
		},
		want: `{"level":"trace","strval":"bar","intval":123}` + "\n",
	}, {
		name: "trace one-field",
		logfunc: func(logger CtxLogger) {
			logger.Trace(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"trace","strval":"bar","intval":123,"foo":"bar"}` + "\n",
	}, {
		name: "trace two-field",
		logfunc: func(logger CtxLogger) {
			logger.Trace(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"trace","strval":"bar","intval":123,"foo":"bar","n":123}` + "\n",
	}, {
		name: "debug empty",
		logfunc: func(logger CtxLogger) {
			logger.Debug(ctx).Msg("")
		},
		want: `{"level":"debug","strval":"bar","intval":123}` + "\n",
	}, {
		name: "debug one-field",
		logfunc: func(logger CtxLogger) {
			logger.Debug(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"debug","strval":"bar","intval":123,"foo":"bar"}` + "\n",
	}, {
		name: "debug two-field",
		logfunc: func(logger CtxLogger) {
			logger.Debug(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"debug","strval":"bar","intval":123,"foo":"bar","n":123}` + "\n",
	}, {
		name: "info empty",
		logfunc: func(logger CtxLogger) {
			logger.Info(ctx).Msg("")
		},
		want: `{"level":"info","strval":"bar","intval":123}` + "\n",
	}, {
		name: "info one-field",
		logfunc: func(logger CtxLogger) {
			logger.Info(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"info","strval":"bar","intval":123,"foo":"bar"}` + "\n",
	}, {
		name: "info two-field",
		logfunc: func(logger CtxLogger) {
			logger.Info(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"info","strval":"bar","intval":123,"foo":"bar","n":123}` + "\n",
	}, {
		name: "warn empty",
		logfunc: func(logger CtxLogger) {
			logger.Warn(ctx).Msg("")
		},
		want: `{"level":"warn","strval":"bar","intval":123}` + "\n",
	}, {
		name: "warn one-field",
		logfunc: func(logger CtxLogger) {
			logger.Warn(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"warn","strval":"bar","intval":123,"foo":"bar"}` + "\n",
	}, {
		name: "warn two-field",
		logfunc: func(logger CtxLogger) {
			logger.Warn(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"warn","strval":"bar","intval":123,"foo":"bar","n":123}` + "\n",
	}, {
		name: "error empty",
		logfunc: func(logger CtxLogger) {
			logger.Error(ctx).Msg("")
		},
		want: `{"level":"error","strval":"bar","intval":123}` + "\n",
	}, {
		name: "error one-field",
		logfunc: func(logger CtxLogger) {
			logger.Error(ctx).Str("foo", "bar").Msg("")
		},
		want: `{"level":"error","strval":"bar","intval":123,"foo":"bar"}` + "\n",
	}, {
		name: "error two-field",
		logfunc: func(logger CtxLogger) {
			logger.Error(ctx).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"error","strval":"bar","intval":123,"foo":"bar","n":123}` + "\n",
	}, {
		name: "err empty with error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, cerrors.New("foo")).Msg("")
		},
		want: `{"level":"error","strval":"bar","intval":123,"stack":\[{"func":"github.com/conduitio/conduit/pkg/foundation/log.TestCtxLoggerWithHooks.func\d*","file":".*/conduit/pkg/foundation/log/ctxlogger_test.go","line":\d*}\],"error":"foo"}\n`,
	}, {
		name: "err one-field with error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, cerrors.New("foo")).Str("foo", "bar").Msg("")
		},
		want: `{"level":"error","strval":"bar","intval":123,"stack":\[{"func":"github.com/conduitio/conduit/pkg/foundation/log.TestCtxLoggerWithHooks.func\d*","file":".*/conduit/pkg/foundation/log/ctxlogger_test.go","line":\d*}],"error":"foo","foo":"bar"}\n`,
	}, {
		name: "err two-field with error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, cerrors.New("foo")).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"error","strval":"bar","intval":123,"stack":\[{"func":"github.com/conduitio/conduit/pkg/foundation/log.TestCtxLoggerWithHooks.func\d*","file":".*/conduit/pkg/foundation/log/ctxlogger_test.go","line":\d*}\],"error":"foo","foo":"bar","n":123}\n`,
	}, {
		name: "err empty without error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, nil).Msg("")
		},
		want: `{"level":"info","strval":"bar","intval":123}` + "\n",
	}, {
		name: "err one-field without error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, nil).Str("foo", "bar").Msg("")
		},
		want: `{"level":"info","strval":"bar","intval":123,"foo":"bar"}` + "\n",
	}, {
		name: "err two-field without error",
		logfunc: func(logger CtxLogger) {
			logger.Err(ctx, nil).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"info","strval":"bar","intval":123,"foo":"bar","n":123}` + "\n",
	}, {
		name: "with level empty",
		logfunc: func(logger CtxLogger) {
			logger.WithLevel(ctx, zerolog.DebugLevel).Msg("")
		},
		want: `{"level":"debug","strval":"bar","intval":123}` + "\n",
	}, {
		name: "with level one-field",
		logfunc: func(logger CtxLogger) {
			logger.WithLevel(ctx, zerolog.InfoLevel).Str("foo", "bar").Msg("")
		},
		want: `{"level":"info","strval":"bar","intval":123,"foo":"bar"}` + "\n",
	}, {
		name: "with level two-field",
		logfunc: func(logger CtxLogger) {
			logger.WithLevel(ctx, zerolog.WarnLevel).
				Str("foo", "bar").
				Int("n", 123).
				Msg("")
		},
		want: `{"level":"warn","strval":"bar","intval":123,"foo":"bar","n":123}` + "\n",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			logger := New(zerolog.New(&out).With().Stack().Logger())
			logger = logger.CtxHook(CtxHookFunc(strValCtxHook))
			logger = logger.CtxHook(CtxHookFunc(intValCtxHook))
			tc.logfunc(logger)
			got := out.String()
			matched, err := regexp.Match(tc.want, []byte(got))
			if !matched || err != nil {
				t.Errorf("invalid log output:\ngot:  %v\nwant: %v", got, tc.want)
			}
		})
	}
}

func TestCtxLoggerFatal(t *testing.T) {
	// approach for testing os.Exit taken from https://stackoverflow.com/a/33404435/1212463
	if os.Getenv("CTXLOGGER_FATAL") == "1" {
		// actual execution of test
		var out bytes.Buffer
		logger := New(zerolog.New(&out))
		logger.Fatal(context.Background()).Msg("this should stop execution")
		t.Fatal("should not reach code after emitting fatal log")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestCtxLoggerFatal") // nolint:gosec // only a test
	cmd.Env = append(os.Environ(), "CTXLOGGER_FATAL=1")
	err := cmd.Run()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 1 {
		t.Fatalf("process ran with err %v, want exit status 1", err)
	}
}

func TestCtxLoggerPanic(t *testing.T) {
	defer func() {
		got := recover()
		if got == nil {
			t.Fatal("expected a panic")
		}
		if want := "this should cause a panic"; got != want {
			t.Errorf("invalid log output:\ngot:  %v\nwant: %v", got, want)
		}
	}()

	var out bytes.Buffer
	logger := New(zerolog.New(&out))
	logger.Panic(context.Background()).Msg("this should cause a panic")
	t.Fatal("should not reach code after emitting panic log")
}

func TestDisabledEvent(t *testing.T) {
	var out bytes.Buffer
	logger := New(zerolog.New(&out).Level(zerolog.WarnLevel))
	logger.CtxHook(CtxHookFunc(func(ctx context.Context, e *zerolog.Event, l zerolog.Level) {
		t.Fatal("did not expect ctx hook to be called")
	}))
	logger.Info(context.Background()).Msg("this log should not be written")
	if got, want := out.String(), ""; got != want {
		t.Errorf("invalid log output:\ngot:  %v\nwant: %v", got, want)
	}
}
