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

	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins/s3/destination/writer"
)

// Destination S3 Connector persists records to an S3 storage. The records are usually
// buffered and written in batches for performance reasons. The buffer size is
// determined by config.
type Destination struct {
	sdk.UnimplementedDestination

	Buffer       []sdk.Record
	AckFuncCache []sdk.AckFunc
	Config       Config
	Error        error
	Writer       writer.Writer
	Mutex        *sync.Mutex
}

func NewDestination() sdk.Destination {
	return &Destination{}
}

// Configure parses and initializes the config.
func (d *Destination) Configure(ctx context.Context, cfg map[string]string) error {
	configuration, err := Parse(cfg)

	if err != nil {
		return err
	}

	d.Config = configuration

	return nil
}

// Open makes sure everything is prepared to receive records.
func (d *Destination) Open(ctx context.Context) error {
	d.Mutex = &sync.Mutex{}

	// initializing the buffer
	d.Buffer = make([]sdk.Record, 0, d.Config.BufferSize)
	d.AckFuncCache = make([]sdk.AckFunc, 0, d.Config.BufferSize)

	// initializing the writer
	writer, err := writer.NewS3(ctx, &writer.S3Config{
		AccessKeyID:     d.Config.AWSAccessKeyID,
		SecretAccessKey: d.Config.AWSSecretAccessKey,
		Region:          d.Config.AWSRegion,
		Bucket:          d.Config.AWSBucket,
		KeyPrefix:       d.Config.Prefix,
	})

	if err != nil {
		return err
	}

	d.Writer = writer
	return nil
}

// WriteAsync writes a record into a Destination. Typically Destination maintains an in-memory
// buffer and doesn't actually perform a write until the buffer has enough
// records in it. This is done for performance reasons.
func (d *Destination) WriteAsync(ctx context.Context, r sdk.Record, ackFunc sdk.AckFunc) error {
	// If either Destination or Writer have encountered an error, there's no point in
	// accepting more records. We better signal the error up the stack and force
	// the server to maybe re-instantiate plugin or do something else about it.
	if d.Error != nil {
		return d.Error
	}

	d.Mutex.Lock()
	defer d.Mutex.Unlock()

	d.Buffer = append(d.Buffer, r)
	d.AckFuncCache = append(d.AckFuncCache, ackFunc)

	if len(d.Buffer) >= int(d.Config.BufferSize) {
		bufferedRecords := d.Buffer
		d.Buffer = d.Buffer[:0]

		// write batch into S3
		err := d.Writer.Write(ctx, &writer.Batch{
			Records: bufferedRecords,
			Format:  d.Config.Format,
		})
		if err != nil {
			d.Error = err
		}

		// call all the written records' ackFunctions
		for _, ack := range d.AckFuncCache {
			err := ack(d.Error)
			if err != nil {
				return err
			}
		}
		// clear ackFunc cache
		d.AckFuncCache = d.AckFuncCache[:0]
	}

	return d.Error
}

// Teardown gracefully disconnects the client
func (d *Destination) Teardown(ctx context.Context) error {
	return nil // TODO
}

func (d *Destination) Flush(ctx context.Context) error {
	bufferedRecords := d.Buffer
	d.Buffer = d.Buffer[:0]

	// write batch into S3
	err := d.Writer.Write(ctx, &writer.Batch{
		Records: bufferedRecords,
		Format:  d.Config.Format,
	})
	if err != nil {
		d.Error = err
	}

	// call all the written records' ackFunctions
	for _, ack := range d.AckFuncCache {
		err := ack(d.Error)
		if err != nil {
			return err
		}
	}
	d.AckFuncCache = d.AckFuncCache[:0]
	return nil
}
