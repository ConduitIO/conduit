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

package file

import (
	"bufio"
	"context"
	"log"
	"os"
	"strconv"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
)

// Destination connector
type Destination struct {
	Scanner *bufio.Scanner
	File    *os.File
	Config  map[string]string
}

func (c *Destination) Open(ctx context.Context, config plugins.Config) error {
	cfg := config.Settings
	path, ok := cfg[ConfigPath]
	if !ok {
		return cerrors.New("path does not exist")
	}

	file, err := openOrCreate(path)
	if err != nil {
		log.Printf("ErrOpen: %+v", err)
		return err
	}

	c.Scanner = bufio.NewScanner(file)
	c.File = file
	c.Config = cfg
	return nil
}

func (c *Destination) Teardown() error {
	return c.File.Close()
}

func (c *Destination) Validate(cfg plugins.Config) error {
	return nil
}

func (c *Destination) Write(ctx context.Context, r record.Record) (record.Position, error) {
	b := r.Payload.Bytes()

	n, err := c.File.Write(append(b, byte('\n')))
	if err != nil {
		return record.Position{}, cerrors.Errorf("fileconn write: write error: %w", err)
	}

	// TODO figure out actual position of written record
	bs := []byte(strconv.Itoa(n))

	return bs, nil
}

func openOrCreate(path string) (*os.File, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		file, err := os.Create(path)
		if err != nil {
			return nil, err
		}

		return file, err
	}
	if err != nil {
		return nil, err
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	return file, nil
}
