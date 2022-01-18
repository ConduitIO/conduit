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

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/nxadm/tail"
)

// Source connector
type Source struct {
	sdk.UnimplementedSource

	tail   *tail.Tail
	config map[string]string

	lastPosition sdk.Position
	nextOffset   int64
	nextPosition sdk.Position
}

func NewSource() sdk.Source {
	return &Source{}
}

func (s *Source) Configure(ctx context.Context, m map[string]string) error {
	err := s.validateConfig(m)
	if err != nil {
		return err
	}
	s.config = m
	return nil
}

func (s *Source) Open(ctx context.Context, position sdk.Position) error {
	return s.seek(position)
}

func (s *Source) Read(ctx context.Context) (sdk.Record, error) {
	select {
	case line := <-s.tail.Lines:
		s.lastPosition = s.nextPosition
		// calculate next offset by adding the bytes in the current line plus
		// 1 byte for the line break
		s.nextOffset += int64(len(line.Text)) + 1
		s.nextPosition = sdk.Position(strconv.FormatInt(s.nextOffset, 10))
		return sdk.Record{
			Position:  s.lastPosition,
			CreatedAt: line.Time,
			Payload:   sdk.RawData(line.Text),
		}, nil
	case <-ctx.Done():
		return sdk.Record{}, ctx.Err()
	}
}

func (s *Source) Ack(ctx context.Context, position sdk.Position) error {
	return nil // no ack needed
}

func (s *Source) Teardown(ctx context.Context) error {
	if s.tail != nil {
		return s.tail.Stop()
	}
	return nil
}

func (s *Source) seek(p sdk.Position) error {
	if bytes.Equal(s.lastPosition, p) && s.tail != nil {
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
		s.config[ConfigPath],
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

	s.nextOffset = offset
	if p != nil {
		// read line at position so that Read returns next line
		line := <-t.Lines
		s.nextOffset += int64(len(line.Text)) + 1
	}
	s.nextPosition = sdk.Position(strconv.FormatInt(s.nextOffset, 10))
	s.lastPosition = p
	s.tail = t

	return nil
}

func (s *Source) validateConfig(cfg map[string]string) error {
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
