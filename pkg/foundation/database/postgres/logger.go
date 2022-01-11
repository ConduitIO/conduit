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
	"github.com/jackc/pgx/v4"
	"github.com/rs/zerolog"
)

var pgxLogLevelMapping = map[pgx.LogLevel]zerolog.Level{
	pgx.LogLevelNone:  zerolog.NoLevel,
	pgx.LogLevelError: zerolog.ErrorLevel,
	pgx.LogLevelWarn:  zerolog.WarnLevel,
	pgx.LogLevelInfo:  zerolog.DebugLevel, // all queries are logged with level Info, lower it to Debug
	pgx.LogLevelDebug: zerolog.DebugLevel,
	pgx.LogLevelTrace: zerolog.TraceLevel,
}

type logger log.CtxLogger

func (l logger) Log(ctx context.Context, level pgx.LogLevel, msg string, data map[string]interface{}) {
	zlevel := pgxLogLevelMapping[level] // default is 0, debug level
	(log.CtxLogger)(l).WithLevel(ctx, zlevel).Fields(data).Msg(msg)
}
