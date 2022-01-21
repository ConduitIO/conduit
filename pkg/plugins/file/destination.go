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
	"os"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
)

// Destination connector
type Destination struct {
	sdk.UnimplementedDestination

	config map[string]string

	scanner *bufio.Scanner
	file    *os.File
}

func NewDestination() sdk.Destination {
	return &Destination{}
}

func (d *Destination) Configure(ctx context.Context, m map[string]string) error {
	err := d.validateConfig(m)
	if err != nil {
		return err
	}
	d.config = m
	return nil
}

func (d *Destination) Open(ctx context.Context) error {
	file, err := d.openOrCreate(d.config[ConfigPath])
	if err != nil {
		return err
	}

	d.scanner = bufio.NewScanner(file)
	d.file = file
	return nil
}

func (d *Destination) Write(ctx context.Context, r sdk.Record, ackFunc sdk.AckFunc) error {
	_, err := d.file.Write(append(r.Payload.Bytes(), byte('\n')))
	return ackFunc(err)
}

func (d *Destination) Flush(ctx context.Context) error {
	return d.file.Sync()
}

func (d *Destination) Teardown(ctx context.Context) error {
	if d.file != nil {
		return d.file.Close()
	}
	return nil
}

func (d *Destination) openOrCreate(path string) (*os.File, error) {
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

func (d *Destination) validateConfig(cfg map[string]string) error {
	if _, ok := cfg[ConfigPath]; !ok {
		return requiredConfigErr(ConfigPath)
	}

	// make sure we can stat the file, we don't care if it doesn't exist though
	_, err := os.Stat(cfg[ConfigPath])
	if err != nil && !os.IsNotExist(err) {
		return cerrors.Errorf(
			"%q config value does not contain a valid path: %w",
			ConfigPath, err,
		)
	}

	return nil
}
