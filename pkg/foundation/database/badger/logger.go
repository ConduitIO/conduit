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

package badger

import "github.com/rs/zerolog"

// logger is a wrapper around zerolog.Logger which can be used by badger.
type logger zerolog.Logger

func (b logger) Errorf(s string, args ...interface{}) {
	b.withLevel(zerolog.ErrorLevel, s, args)
}

func (b logger) Warningf(s string, args ...interface{}) {
	b.withLevel(zerolog.WarnLevel, s, args)
}

func (b logger) Infof(s string, args ...interface{}) {
	b.withLevel(zerolog.InfoLevel, s, args)
}

func (b logger) Debugf(s string, args ...interface{}) {
	// DB is quite chatty, we use trace level for their debug messages
	b.withLevel(zerolog.TraceLevel, s, args)
}

func (b *logger) withLevel(level zerolog.Level, s string, args []interface{}) {
	(*zerolog.Logger)(b).WithLevel(level).Msgf(b.cleanMessage(s), args...)
}

func (b logger) cleanMessage(s string) string {
	n := len(s)
	if n > 0 && s[n-1] == '\n' {
		// Trim CR added by badger.
		s = s[0 : n-1]
	}
	return s
}
