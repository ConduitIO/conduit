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
	"io"
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/rs/zerolog"
)

type Format int

const (
	FormatCLI Format = iota
	FormatJSON
)

// ParseFormat converts a format string into a log Format value.
// returns an error if the input string does not match known values.
func ParseFormat(format string) (Format, error) {
	switch format {
	case "cli":
		return FormatCLI, nil
	case "json":
		return FormatJSON, nil
	default:
		return -1, cerrors.Errorf("unsupported log format: %s", format)
	}
}

// GetWriter returns a writer according to the log Format
func GetWriter(f Format) io.Writer {
	var w io.Writer = os.Stdout
	if f == FormatCLI {
		cw := zerolog.NewConsoleWriter()
		cw.TimeFormat = "2006-01-02T15:04:05+00:00"
		w = cw
	}
	return w
}
