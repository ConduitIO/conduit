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
	"bytes"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/record"
)

func Test_ParseRecordPosition(t *testing.T) {
	var positionTests = []struct {
		name    string
		wantErr bool
		in      record.Position
		out     Position
	}{
		{
			name:    "zero position",
			wantErr: false,
			in:      []byte("test_s0"),
			out: Position{
				Key:       "test",
				Type:      TypeSnapshot,
				Timestamp: time.Unix(0, 0),
			},
		},
		{
			name:    "nil position returns empty Position with default values",
			wantErr: false,
			in:      nil,
			out:     Position{},
		},
		{
			name:    "wrong position format returns error",
			wantErr: true,
			in:      []byte("test0"),
			out:     Position{},
		},
		{
			name:    "cdc type position",
			wantErr: false,
			in:      []byte("test_c59"),
			out: Position{
				Key:       "test",
				Type:      TypeCDC,
				Timestamp: time.Unix(59, 0),
			},
		},
		{
			name:    "invalid timestamp returns error",
			wantErr: true,
			in:      []byte("test_88invalid"),
			out:     Position{},
		},
	}

	for _, tt := range positionTests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParseRecordPosition(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseRecordPosition() error = %v, wantErr %v", err, tt.wantErr)
			} else if p != tt.out {
				t.Errorf("ParseRecordPosition(): Got : %v,Expected : %v", p, tt.out)
			}
		})
	}
}

func Test_ToRecordPosition(t *testing.T) {
	var positionTests = []struct {
		name    string
		wantErr bool
		in      Position
		out     record.Position
	}{
		{
			name:    "zero position",
			wantErr: false,
			in: Position{
				Key:       "test",
				Type:      TypeSnapshot,
				Timestamp: time.Unix(0, 0),
			},
			out: []byte("test_s0"),
		},
		{
			name:    "empty position returns the zero value for time.Time",
			wantErr: false,
			in:      Position{},
			out:     []byte("_s-62135596800"),
		},
		{
			name:    "cdc type position",
			wantErr: false,
			in: Position{
				Key:       "test",
				Type:      TypeCDC,
				Timestamp: time.Unix(59, 0),
			},
			out: []byte("test_c59"),
		},
	}

	for _, tt := range positionTests {
		t.Run(tt.name, func(t *testing.T) {
			p := (tt.in).ToRecordPosition()
			if !bytes.Equal(p, tt.out) {
				t.Errorf("ToRecordPosition(): Got : %v,Expected : %v", p, tt.out)
				return
			}
		})
	}
}

func Test_ConvertSnapshotPositionToCDC(t *testing.T) {
	var positionTests = []struct {
		name    string
		wantErr bool
		in      record.Position
		out     record.Position
	}{
		{
			name:    "convert snapshot position to cdc",
			wantErr: false,
			in:      []byte("test_s100"),
			out:     []byte("test_c100"),
		},
		{
			name:    "convert invalid snapshot should produce error",
			wantErr: true,
			in:      []byte("s100"),
			out:     []byte(""),
		},
	}

	for _, tt := range positionTests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ConvertToCDCPosition(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("ConvertToCDCPosition() error = %v, wantErr %v", err, tt.wantErr)
			} else if !bytes.Equal(p, tt.out) {
				t.Errorf("ConvertToCDCPosition(): Got : %v,Expected : %v", p, tt.out)
			}
		})
	}
}
