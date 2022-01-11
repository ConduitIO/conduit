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

package plugins

import (
	"context"
	"io/ioutil"
	"os"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
)

func TestPlugins(t *testing.T) {
	ctx := context.Background()

	client := NewClient(ctx, zerolog.Nop(), "./file/file")
	defer client.Kill()

	connPlug, err := DispenseSource(client)
	if err != nil {
		t.Fatalf("failed to dispense plugin correctly: %+v", err)
	}

	cfg := Config{
		Settings: map[string]string{
			"path": "./fixtures/file-source.txt",
		},
	}
	err = connPlug.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("error opening connector: %+v", err)
	}

	_, err = connPlug.Read(ctx, nil)
	if err != nil {
		t.Fatalf("error reading from connector: %+v", err)
	}
}

func TestSpecifier(t *testing.T) {
	ctx := context.Background()
	client := NewClient(ctx, zerolog.Nop(), "./file/file")
	t.Cleanup(func() {
		client.Kill()
	})

	plug, err := DispenseSpecifier(client)
	assert.Ok(t, err)
	assert.NotNil(t, plug)

	spec, err := plug.Specify()
	assert.Ok(t, err)
	assert.Equal(t, "v0.0.1", spec.Version)
	assert.Equal(t, "Meroxa, Inc.", spec.Author)
	assert.Equal(t, map[string]Parameter{
		"path": {
			Default:     "",
			Description: "the file path where the file destination writes messages",
			Required:    true,
		},
	}, spec.DestinationParams)
	assert.Equal(t, map[string]Parameter{
		"path": {
			Default:     "",
			Description: "the file path from which the file source reads messages",
			Required:    true,
		},
	}, spec.SourceParams)
}

func TestFileSourceConnector(t *testing.T) {
	testCases := []struct {
		name            string
		connectorPath   string // path to the connector binary used for testing
		config          map[string]string
		position        record.Position
		expectedErr     error
		expectedPayload record.Data
	}{
		{
			name: "nil position should read at beginning",
			config: map[string]string{
				"path": "./fixtures/file-source.txt",
			},
			connectorPath:   "./file/file",
			position:        nil,
			expectedErr:     nil,
			expectedPayload: record.RawData{Raw: []byte("1")},
		},
		{
			name: "empty position should read at beginning",
			config: map[string]string{
				"path": "./fixtures/file-source.txt",
			},
			connectorPath:   "./file/file",
			position:        []byte(""),
			expectedErr:     nil,
			expectedPayload: record.RawData{Raw: []byte("1")},
		},
		{
			name: "should return second line",
			config: map[string]string{
				"path": "./fixtures/file-source.txt",
			},
			connectorPath:   "./file/file",
			position:        []byte("0"),
			expectedErr:     nil,
			expectedPayload: record.RawData{Raw: []byte("2")},
		},
		{
			name: "should return third line",
			config: map[string]string{
				"path": "./fixtures/file-source.txt",
			},
			connectorPath:   "./file/file",
			position:        []byte("2"),
			expectedErr:     nil,
			expectedPayload: record.RawData{Raw: []byte("3")},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			client := NewClient(ctx, zerolog.Nop(), tt.connectorPath)
			defer client.Kill()

			connPlug, err := DispenseSource(client)
			if err != nil {
				t.Errorf("failed to dispense plugin correctly: %+v", err)
			}

			err = connPlug.Open(ctx, Config{Settings: tt.config})
			if err != nil {
				t.Errorf("failed to open connector plugin: %+v\n", err)
			}

			r, err := connPlug.Read(ctx, tt.position)
			if err != nil {
				if diff := cmp.Diff(tt.expectedErr, err); diff != "" {
					t.Errorf("expected error %+v - got error %+v", tt.expectedErr, err)
				}
			}

			if diff := cmp.Diff(tt.expectedPayload, r.Payload); diff != "" {
				t.Errorf("expected payload %+v - got payload %+v", tt.expectedPayload, r.Payload)
			}
		})
	}
}

func TestFileDestinationConnector(t *testing.T) {
	testCases := []struct {
		name          string
		connectorPath string
		config        map[string]string
		record        record.Record
		expectedErr   error
	}{
		{
			name:          "basic file writer test",
			connectorPath: "./file/file",
			config: map[string]string{
				"path": t.TempDir() + "/destination.txt",
			},
			record: record.Record{
				Payload: record.RawData{Raw: []byte("BEEF")},
			},
			expectedErr: nil,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			// start the test clean and finish it clean
			cleanWrite(t, tt.config["path"])

			ctx := context.Background()

			client := NewClient(ctx, zerolog.Nop(), tt.connectorPath)
			defer client.Kill()

			destination, err := DispenseDestination(client)
			if err != nil {
				t.Errorf("failed to dispense plugin correctly: %+v", err)
			}

			err = destination.Open(ctx, Config{Settings: tt.config})
			if err != nil {
				t.Errorf("failed to open connector plugin: %+v", err)
			}

			_, err = destination.Write(ctx, tt.record) // TODO assert position
			if diff := cmp.Diff(tt.expectedErr, err); diff != "" {
				t.Errorf("expected %+v - got %+v", tt.expectedErr, err)
			}

			d, err := ioutil.ReadFile(tt.config["path"])
			if err != nil {
				t.Errorf("failed to write to test file")
			}

			if diff := cmp.Diff(d, append(tt.record.Payload.Bytes(), '\n')); diff != "" {
				t.Errorf("wanted %+v - got %+v", tt.record.Payload.Bytes(), d)
			}
		})
	}
}

func cleanWrite(t *testing.T, path string) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return
	}

	err = os.Remove(path)
	if err != nil {
		t.Errorf("failed to properly clean test environment")
	}
}
