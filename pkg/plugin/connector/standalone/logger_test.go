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

package standalone

import (
	"bytes"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/hashicorp/go-hclog"
	"github.com/rs/zerolog"
)

func TestHCLogger(t *testing.T) {
	testCases := []struct {
		name    string
		logfunc func(hcLogger)
		want    string
	}{{
		name: "log empty",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.NoLevel, "")
		},
		want: `{}` + "\n",
	}, {
		name: "log one-field",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.NoLevel, "", "foo", "bar")
		},
		want: `{"foo":"bar"}` + "\n",
	}, {
		name: "log two-field",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.NoLevel,
				"",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"foo":"bar","n":123}` + "\n",
	}, {
		name: "log two-field with message",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.NoLevel,
				"hello",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"foo":"bar","n":123,"message":"hello"}` + "\n",
	}, {
		name: "log odd number of fields",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.NoLevel,
				"",
				"foo", "bar",
				"n", 123,
				"another",
			)
		},
		want: `{"foo":"bar","n":123,"":"another"}` + "\n",
	}, {
		name: "log non-string field",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.NoLevel,
				"",
				"foo", "bar",
				123, "n",
			)
		},
		want: `{"foo":"bar","123":"n"}` + "\n",
	}, {
		name: "trace empty",
		logfunc: func(logger hcLogger) {
			logger.Trace("")
		},
		want: `{"level":"trace"}` + "\n",
	}, {
		name: "trace one-field",
		logfunc: func(logger hcLogger) {
			logger.Trace("", "foo", "bar")
		},
		want: `{"level":"trace","foo":"bar"}` + "\n",
	}, {
		name: "trace two-field",
		logfunc: func(logger hcLogger) {
			logger.Trace(
				"",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"trace","foo":"bar","n":123}` + "\n",
	}, {
		name: "trace two-field with message",
		logfunc: func(logger hcLogger) {
			logger.Trace(
				"hello",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"trace","foo":"bar","n":123,"message":"hello"}` + "\n",
	}, {
		name: "trace odd number of fields",
		logfunc: func(logger hcLogger) {
			logger.Trace(
				"",
				"foo", "bar",
				"n", 123,
				"another",
			)
		},
		want: `{"level":"trace","foo":"bar","n":123,"":"another"}` + "\n",
	}, {
		name: "debug empty",
		logfunc: func(logger hcLogger) {
			logger.Debug("")
		},
		want: `{"level":"debug"}` + "\n",
	}, {
		name: "debug one-field",
		logfunc: func(logger hcLogger) {
			logger.Debug("", "foo", "bar")
		},
		want: `{"level":"debug","foo":"bar"}` + "\n",
	}, {
		name: "debug two-field",
		logfunc: func(logger hcLogger) {
			logger.Debug(
				"",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"debug","foo":"bar","n":123}` + "\n",
	}, {
		name: "debug two-field with message",
		logfunc: func(logger hcLogger) {
			logger.Debug(
				"hello",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"debug","foo":"bar","n":123,"message":"hello"}` + "\n",
	}, {
		name: "debug odd number of fields",
		logfunc: func(logger hcLogger) {
			logger.Debug(
				"",
				"foo", "bar",
				"n", 123,
				"another",
			)
		},
		want: `{"level":"debug","foo":"bar","n":123,"":"another"}` + "\n",
	}, {
		name: "info empty",
		logfunc: func(logger hcLogger) {
			logger.Info("")
		},
		want: `{"level":"info"}` + "\n",
	}, {
		name: "info one-field",
		logfunc: func(logger hcLogger) {
			logger.Info("", "foo", "bar")
		},
		want: `{"level":"info","foo":"bar"}` + "\n",
	}, {
		name: "info two-field",
		logfunc: func(logger hcLogger) {
			logger.Info(
				"",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"info","foo":"bar","n":123}` + "\n",
	}, {
		name: "info two-field with message",
		logfunc: func(logger hcLogger) {
			logger.Info(
				"hello",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"info","foo":"bar","n":123,"message":"hello"}` + "\n",
	}, {
		name: "info odd number of fields",
		logfunc: func(logger hcLogger) {
			logger.Info(
				"",
				"foo", "bar",
				"n", 123,
				"another",
			)
		},
		want: `{"level":"info","foo":"bar","n":123,"":"another"}` + "\n",
	}, {
		name: "warn empty",
		logfunc: func(logger hcLogger) {
			logger.Warn("")
		},
		want: `{"level":"warn"}` + "\n",
	}, {
		name: "warn one-field",
		logfunc: func(logger hcLogger) {
			logger.Warn("", "foo", "bar")
		},
		want: `{"level":"warn","foo":"bar"}` + "\n",
	}, {
		name: "warn two-field",
		logfunc: func(logger hcLogger) {
			logger.Warn(
				"",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"warn","foo":"bar","n":123}` + "\n",
	}, {
		name: "warn two-field with message",
		logfunc: func(logger hcLogger) {
			logger.Warn(
				"hello",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"warn","foo":"bar","n":123,"message":"hello"}` + "\n",
	}, {
		name: "warn odd number of fields",
		logfunc: func(logger hcLogger) {
			logger.Warn(
				"",
				"foo", "bar",
				"n", 123,
				"another",
			)
		},
		want: `{"level":"warn","foo":"bar","n":123,"":"another"}` + "\n",
	}, {
		name: "error empty",
		logfunc: func(logger hcLogger) {
			logger.Error("")
		},
		want: `{"level":"error"}` + "\n",
	}, {
		name: "error one-field",
		logfunc: func(logger hcLogger) {
			logger.Error("", "foo", "bar")
		},
		want: `{"level":"error","foo":"bar"}` + "\n",
	}, {
		name: "error two-field",
		logfunc: func(logger hcLogger) {
			logger.Error(
				"",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"error","foo":"bar","n":123}` + "\n",
	}, {
		name: "error two-field with message",
		logfunc: func(logger hcLogger) {
			logger.Error(
				"hello",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"error","foo":"bar","n":123,"message":"hello"}` + "\n",
	}, {
		name: "error odd number of fields",
		logfunc: func(logger hcLogger) {
			logger.Error(
				"",
				"foo", "bar",
				"n", 123,
				"another",
			)
		},
		want: `{"level":"error","foo":"bar","n":123,"":"another"}` + "\n",
	}, {
		name: "err empty with error",
		logfunc: func(logger hcLogger) {
			logger.Error("", "error", cerrors.New("foo"))
		},
		want: `{"level":"error","error":"foo"}` + "\n",
	}, {
		name: "err one-field with error",
		logfunc: func(logger hcLogger) {
			logger.Error(
				"",
				"error", cerrors.New("foo"),
				"foo", "bar")
		},
		want: `{"level":"error","error":"foo","foo":"bar"}` + "\n",
	}, {
		name: "err two-field with error",
		logfunc: func(logger hcLogger) {
			logger.Error(
				"",
				"error", cerrors.New("foo"),
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"error","error":"foo","foo":"bar","n":123}` + "\n",
	}, {
		name: "with level empty",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.Debug, "")
		},
		want: `{"level":"debug"}` + "\n",
	}, {
		name: "with level one-field",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.Info, "", "foo", "bar")
		},
		want: `{"level":"info","foo":"bar"}` + "\n",
	}, {
		name: "with level two-field",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.Warn,
				"",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"warn","foo":"bar","n":123}` + "\n",
	}, {
		name: "with level two-field with message",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.Error,
				"hello",
				"foo", "bar",
				"n", 123,
			)
		},
		want: `{"level":"error","foo":"bar","n":123,"message":"hello"}` + "\n",
	}, {
		name: "with level odd number of fields",
		logfunc: func(logger hcLogger) {
			logger.Log(hclog.Trace,
				"hello",
				"foo", "bar",
				"n", 123,
				"another",
			)
		},
		want: `{"level":"trace","foo":"bar","n":123,"":"another","message":"hello"}` + "\n",
	}}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			logger := hcLogger{logger: zerolog.New(&out)}
			tc.logfunc(logger)
			got := out.String()
			if got != tc.want {
				t.Errorf("invalid log output:\ngot:  %v\nwant: %v", got, tc.want)
			}
		})
	}
}
