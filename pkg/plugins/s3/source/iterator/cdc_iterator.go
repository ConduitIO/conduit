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

package iterator

import (
	"context"
	"io/ioutil"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins/s3/source/position"
	"github.com/conduitio/conduit/pkg/record"
	"gopkg.in/tomb.v2"
)

// CDCIterator scans the bucket periodically and detects changes made to it.
type CDCIterator struct {
	bucket        string
	client        *s3.Client
	buffer        chan record.Record
	ticker        *time.Ticker
	lastModified  time.Time
	caches        chan []CacheEntry
	isTruncated   bool
	nextKeyMarker *string
	tomb          *tomb.Tomb
}

type CacheEntry struct {
	key          string
	lastModified time.Time
	deleteMarker bool
}

// NewCDCIterator returns a CDCIterator and starts the process of listening to changes every pollingPeriod.
func NewCDCIterator(bucket string, pollingPeriod time.Duration, client *s3.Client, from time.Time) (*CDCIterator, error) {
	cdc := CDCIterator{
		bucket:        bucket,
		client:        client,
		buffer:        make(chan record.Record, 1),
		caches:        make(chan []CacheEntry),
		ticker:        time.NewTicker(pollingPeriod),
		isTruncated:   true,
		nextKeyMarker: nil,
		tomb:          &tomb.Tomb{},
		lastModified:  from,
	}

	// start listening to changes
	cdc.tomb.Go(cdc.startCDC)
	cdc.tomb.Go(cdc.flush)

	return &cdc, nil
}

// HasNext returns a boolean that indicates whether the iterator has any objects in the buffer or not.
func (w *CDCIterator) HasNext(ctx context.Context) bool {
	return len(w.buffer) > 0 || !w.tomb.Alive() // if tomb is dead we return true so caller will fetch error with Next
}

// Next returns the next record from the buffer.
func (w *CDCIterator) Next(ctx context.Context) (record.Record, error) {
	select {
	case r := <-w.buffer:
		return r, nil
	case <-w.tomb.Dead():
		return record.Record{}, w.tomb.Err()
	case <-ctx.Done():
		return record.Record{}, ctx.Err()
	}
}

func (w *CDCIterator) Stop() {
	// stop the two goRoutines
	w.ticker.Stop()
	w.tomb.Kill(cerrors.New("cdc iterator is stopped"))
}

// startCDC scans the S3 bucket every polling period for changes
// only detects the changes made after the w.lastModified
func (w *CDCIterator) startCDC() error {
	defer close(w.caches)

	for {
		select {
		case <-w.tomb.Dying():
			return w.tomb.Err()
		case <-w.ticker.C: // detect changes every polling period
			cache := make([]CacheEntry, 0, 1000)
			w.isTruncated = true
			for w.isTruncated {
				latest, err := w.getLatestObjects(w.tomb.Context(nil)) // nolint:staticcheck // SA1012 tomb expects nil
				if err != nil {
					return err
				}
				for _, object := range latest {
					// should "equal" check be here?
					if object.lastModified.Before(w.lastModified) || object.lastModified.Equal(w.lastModified) {
						continue
					}
					cache = append(cache, object)
				}
			}
			if len(cache) == 0 {
				continue
			}
			sort.Slice(cache, func(i, j int) bool {
				return cache[i].lastModified.Before(cache[j].lastModified)
			})

			select {
			case w.caches <- cache:
				w.lastModified = cache[len(cache)-1].lastModified
				// worked fine
			case <-w.tomb.Dying():
				return w.tomb.Err()
			}
		}
	}
}

// flush: go routine that will get the objects from the bucket and flush the detected changes into the buffer.
func (w *CDCIterator) flush() error {
	defer close(w.buffer)

	for {
		select {
		case <-w.tomb.Dying():
			return w.tomb.Err()
		case cache := <-w.caches:
			for i := 0; i < len(cache); i++ {
				entry := cache[i]
				var output record.Record

				if entry.deleteMarker {
					output = w.createDeletedRecord(entry)
				} else {
					object, err := w.client.GetObject(w.tomb.Context(nil), // nolint:staticcheck // SA1012 tomb expects nil
						&s3.GetObjectInput{
							Bucket: aws.String(w.bucket),
							Key:    aws.String(entry.key),
						})
					if err != nil {
						return err
					}
					output, err = w.createRecord(entry, object)
					if err != nil {
						return err
					}
				}

				select {
				case w.buffer <- output:
					// worked fine
				case <-w.tomb.Dying():
					return w.tomb.Err()
				}
			}
		}
	}
}

// getLatestObjects gets all the latest version of objects in S3 bucket
func (w *CDCIterator) getLatestObjects(ctx context.Context) ([]CacheEntry, error) {
	listObjectInput := &s3.ListObjectVersionsInput{ // default is 1000 keys max
		Bucket: aws.String(w.bucket),
	}
	if w.nextKeyMarker != nil {
		listObjectInput.KeyMarker = w.nextKeyMarker
	}
	objects, err := w.client.ListObjectVersions(ctx, listObjectInput)
	if err != nil {
		return nil, cerrors.Errorf("couldn't get latest objects: %w", err)
	}

	cache := make([]CacheEntry, 0, 1000)
	for _, v := range objects.Versions {
		if v.IsLatest {
			cache = append(cache, CacheEntry{key: *v.Key, lastModified: *v.LastModified, deleteMarker: false})
		}
	}
	for _, v := range objects.DeleteMarkers {
		if v.IsLatest {
			cache = append(cache, CacheEntry{key: *v.Key, lastModified: *v.LastModified, deleteMarker: true})
		}
	}

	// check if there is other pages to read
	if objects.IsTruncated {
		w.isTruncated = true
		w.nextKeyMarker = objects.NextKeyMarker
	} else {
		w.isTruncated = false
		w.nextKeyMarker = nil
	}

	return cache, nil
}

// createRecord creates the record for the object fetched from S3 (for updates and inserts)
func (w *CDCIterator) createRecord(entry CacheEntry, object *s3.GetObjectOutput) (record.Record, error) {
	// build record
	rawBody, err := ioutil.ReadAll(object.Body)
	if err != nil {
		return record.Record{}, err
	}
	p := position.Position{
		Key:       entry.key,
		Timestamp: entry.lastModified,
		Type:      position.TypeCDC,
	}

	return record.Record{
		Metadata: map[string]string{
			"content-type": *object.ContentType,
		},
		Position: p.ToRecordPosition(),
		Payload: record.RawData{
			Raw: rawBody,
		},
		Key: record.RawData{
			Raw: []byte(entry.key),
		},
		CreatedAt: *object.LastModified,
	}, nil
}

// createDeletedRecord creates the record for the object fetched from S3 (for deletes)
func (w *CDCIterator) createDeletedRecord(entry CacheEntry) record.Record {
	p := position.Position{
		Key:       entry.key,
		Timestamp: entry.lastModified,
		Type:      position.TypeCDC,
	}
	return record.Record{
		Metadata: map[string]string{
			"action": "delete",
		},
		Position: p.ToRecordPosition(),
		Key: record.RawData{
			Raw: []byte(entry.key),
		},
		CreatedAt: entry.lastModified,
	}
}
