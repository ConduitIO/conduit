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

package position

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record"
)

const (
	TypeSnapshot Type = iota
	TypeCDC
)

const (
	snapshotPrefixChar = 's'
	cdcPrefixChar      = 'c'
)

type Type int

type Position struct {
	Key       string
	Timestamp time.Time
	Type      Type
}

func ParseRecordPosition(p record.Position) (Position, error) {
	if p == nil {
		// empty Position would have the fields with their default values
		return Position{}, nil
	}
	s := string(p)
	index := strings.LastIndex(s, "_")
	if index == -1 {
		return Position{}, cerrors.New("invalid position format, no '_' found")
	}
	seconds, err := strconv.ParseInt(s[index+2:], 10, 64)
	if err != nil {
		return Position{}, cerrors.Errorf("could not parse the position timestamp: %w", err)
	}

	if s[index+1] != cdcPrefixChar && s[index+1] != snapshotPrefixChar {
		return Position{}, cerrors.Errorf("invalid position format, no '%c' or '%c' after '_'\n", snapshotPrefixChar, cdcPrefixChar)
	}
	pType := TypeSnapshot
	if s[index+1] == cdcPrefixChar {
		pType = TypeCDC
	}

	return Position{
		Key:       s[:index],
		Timestamp: time.Unix(seconds, 0),
		Type:      pType,
	}, err
}

func (p Position) ToRecordPosition() record.Position {
	char := snapshotPrefixChar
	if p.Type == TypeCDC {
		char = cdcPrefixChar
	}
	return []byte(fmt.Sprintf("%s_%c%d", p.Key, char, p.Timestamp.Unix()))
}

func ConvertToCDCPosition(p record.Position) (record.Position, error) {
	cdcPos, err := ParseRecordPosition(p)
	if err != nil {
		return record.Position{}, err
	}
	cdcPos.Type = TypeCDC
	return cdcPos.ToRecordPosition(), nil
}
