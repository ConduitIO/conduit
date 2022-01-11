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

package generator

import (
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"
)

const (
	RecordCount = "recordCount"
	ReadTime    = "readTime"
	Fields      = "fields"
)

var knownFieldTypes = []string{"int", "string", "time", "bool"}

type Config struct {
	RecordCount int64
	ReadTime    time.Duration
	Fields      map[string]string
}

func Parse(config plugins.Config) (Config, error) {
	parsed := Config{}
	// default value
	parsed.RecordCount = -1
	if recCount, ok := config.Settings[RecordCount]; ok {
		recCountParsed, err := strconv.ParseInt(recCount, 10, 64)
		if err != nil {
			return Config{}, cerrors.Errorf("invalid record count: %w", err)
		}
		parsed.RecordCount = recCountParsed
	}
	if readTime, ok := config.Settings[ReadTime]; ok {
		readTimeParsed, err := time.ParseDuration(readTime)
		if err != nil || readTimeParsed < 0 {
			return Config{}, cerrors.Errorf("invalid processing time: %w", err)
		}
		parsed.ReadTime = readTimeParsed
	}
	fieldsConcat := config.Settings[Fields]
	if fieldsConcat == "" {
		return Config{}, cerrors.New("no fields specified")
	}

	fieldsMap := map[string]string{}
	fields := strings.Split(fieldsConcat, ",")
	for _, field := range fields {
		if strings.Trim(field, " ") == "" {
			return Config{}, cerrors.Errorf("got empty field spec in %q", field)
		}
		fieldSpec := strings.Split(field, ":")
		if validFieldSpec(fieldSpec) {
			return Config{}, cerrors.Errorf("invalid field spec %q", field)
		}
		if !knownType(fieldSpec[1]) {
			return Config{}, cerrors.Errorf("unknown data type in %q", field)
		}
		fieldsMap[fieldSpec[0]] = fieldSpec[1]
	}
	parsed.Fields = fieldsMap

	return parsed, nil
}

func validFieldSpec(fieldSpec []string) bool {
	return len(fieldSpec) != 2 || strings.Trim(fieldSpec[0], " ") == "" || strings.Trim(fieldSpec[1], " ") == ""
}

func knownType(typeString string) bool {
	for _, t := range knownFieldTypes {
		if strings.ToLower(typeString) == t {
			return true
		}
	}
	return false
}
