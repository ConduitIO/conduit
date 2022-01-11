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

package destination

import (
	"context"
	"sync"

	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/plugins/s3/destination/writer"
	"github.com/conduitio/conduit/pkg/record"
)

// Destination S3 Connector persists records to an S3 storage. The records are usually
// buffered and written in batches for performance reasons. The buffer size is
// determined by config.
type Destination struct {
	Buffer []record.Record
	Config Config
	Error  error
	Writer writer.Writer
	Mutex  *sync.Mutex
}

// Open parses and initializes the config and makes sure everything is prepared
// to receive records.
func (s *Destination) Open(ctx context.Context, cfg plugins.Config) error {
	configuration, err := Parse(cfg.Settings)

	if err != nil {
		return err
	}

	s.Config = configuration
	s.Mutex = &sync.Mutex{}

	// initializing the buffer
	s.Buffer = make([]record.Record, 0, s.Config.BufferSize)

	// initializing the writer
	writer, err := writer.NewS3(ctx, &writer.S3Config{
		AccessKeyID:     s.Config.AWSAccessKeyID,
		SecretAccessKey: s.Config.AWSSecretAccessKey,
		Region:          s.Config.AWSRegion,
		Bucket:          s.Config.AWSBucket,
		KeyPrefix:       s.Config.Prefix,
	})

	if err != nil {
		return err
	}

	s.Writer = writer

	return nil
}

// Teardown gracefully disconnects the client
func (s *Destination) Teardown() error {
	return nil // TODO
}

// Validate takes config and returns an error if some values are missing or
// incorrect.
func (s *Destination) Validate(cfg plugins.Config) error {
	_, err := Parse(cfg.Settings)
	return err
}

// Write writes a record into a Destination. Typically Destination maintains an in-memory
// buffer and doesn't actually perform a write until the buffer has enough
// records in it. This is done for performance reasons.
func (s *Destination) Write(ctx context.Context, r record.Record) (record.Position, error) {
	// If either Destination or Writer have encountered an error, there's no point in
	// accepting more records. We better signal the error up the stack and force
	// the server to maybe re-instantiate plugin or do something else about it.
	if s.Error != nil {
		return nil, s.Error
	}

	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	s.Buffer = append(s.Buffer, r)

	if len(s.Buffer) >= int(s.Config.BufferSize) {
		bufferedRecords := s.Buffer
		s.Buffer = make([]record.Record, 0, s.Config.BufferSize)

		err := s.Writer.Write(ctx, &writer.Batch{
			Records: bufferedRecords,
			Format:  s.Config.Format,
		})

		if err != nil {
			s.Error = err
		}
	}

	return s.Writer.LastPosition(), s.Error
}
