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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/nxadm/tail"
)

// Source connector
type Source struct {
	Tail   *tail.Tail
	Config map[string]string

	lastPosition record.Position
	nextOffset   int64
	nextPosition record.Position
}

func (c *Source) Ack(ctx context.Context, position record.Position) error {
	return nil // no ack needed
}

func (c *Source) Open(ctx context.Context, config plugins.Config) error {
	err := c.Validate(config)
	if err != nil {
		return err
	}
	c.Config = config.Settings
	return nil
}

func (c *Source) seek(p record.Position) error {
	if bytes.Equal(c.lastPosition, p) && c.Tail != nil {
		// we are at the required position
		return nil
	}

	var offset int64
	if p != nil {
		var err error
		offset, err = strconv.ParseInt(string(p), 10, 64)
		if err != nil {
			return cerrors.Errorf("invalid position %v, expected a number", p)
		}
	}

	fmt.Printf("seeking to position %d\n", offset)

	t, err := tail.TailFile(
		c.Config[ConfigPath],
		tail.Config{
			Follow: true,
			Location: &tail.SeekInfo{
				Offset: offset,
				Whence: io.SeekStart,
			},
			Logger: tail.DiscardingLogger,
		},
	)
	if err != nil {
		return cerrors.Errorf("could not tail file: %w", err)
	}

	c.nextOffset = offset
	if p != nil {
		// read line at position so that Read returns next line
		line := <-t.Lines
		c.nextOffset += int64(len(line.Text)) + 1
	}
	c.nextPosition = record.Position(strconv.FormatInt(c.nextOffset, 10))
	c.lastPosition = p
	c.Tail = t

	return nil
}

func (c *Source) Read(ctx context.Context, p record.Position) (record.Record, error) {
	err := c.seek(p)
	if err != nil {
		return record.Record{}, cerrors.Errorf("could not seek to position: %w", err)
	}

	select {
	case line := <-c.Tail.Lines:
		c.lastPosition = c.nextPosition
		// calculate next offset by adding the bytes in the current line plus
		// 1 byte for the line break
		c.nextOffset += int64(len(line.Text)) + 1
		c.nextPosition = record.Position(strconv.FormatInt(c.nextOffset, 10))
		return record.Record{
			Position:  c.lastPosition,
			CreatedAt: line.Time,
			Payload: record.RawData{
				Raw: []byte(line.Text),
			},
		}, nil
	case <-time.After(time.Millisecond * 100):
		return record.Record{}, plugins.ErrEndData
	}
}
func (c *Source) Teardown() error {
	return c.Tail.Stop()
}

func (c *Source) Validate(cfg plugins.Config) error {
	if _, ok := cfg.Settings[ConfigPath]; !ok {
		return requiredConfigErr(ConfigPath)
	}

	// make sure we can stat the file, we don't care if it doesn't exist though
	_, err := os.Stat(cfg.Settings[ConfigPath])
	if err != nil && !os.IsNotExist(err) {
		return cerrors.Errorf(
			"%q config value does not contain a valid path: %w",
			ConfigPath, err,
		)
	}

	return nil
}
