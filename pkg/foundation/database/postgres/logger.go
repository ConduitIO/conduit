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

package postgres

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/jackc/pgx/v5/tracelog"
	"github.com/rs/zerolog"
)

var pgxLogLevelMapping = map[tracelog.LogLevel]zerolog.Level{
	tracelog.LogLevelNone:  zerolog.NoLevel,
	tracelog.LogLevelError: zerolog.ErrorLevel,
	tracelog.LogLevelWarn:  zerolog.WarnLevel,
	tracelog.LogLevelInfo:  zerolog.DebugLevel, // all queries are logged with level Info, lower it to Debug
	tracelog.LogLevelDebug: zerolog.DebugLevel,
	tracelog.LogLevelTrace: zerolog.TraceLevel,
}

type logger log.CtxLogger

func (l logger) Log(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]interface{}) {
	zlevel := pgxLogLevelMapping[level] // default is 0, debug level
	(log.CtxLogger)(l).WithLevel(ctx, zlevel).Fields(data).Msg(msg)
}
