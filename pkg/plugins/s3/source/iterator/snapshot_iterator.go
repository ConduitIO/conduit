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
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins/s3/source/position"
)

// SnapshotIterator to iterate through S3 objects in a specific bucket.
type SnapshotIterator struct {
	bucket          string
	client          *s3.Client
	paginator       *s3.ListObjectsV2Paginator
	page            *s3.ListObjectsV2Output
	index           int
	maxLastModified time.Time
}

// NewSnapshotIterator takes the s3 bucket, the client, and the position.
// it returns an snapshotIterator starting from the position provided.
func NewSnapshotIterator(bucket string, client *s3.Client, p position.Position) (*SnapshotIterator, error) {
	input := &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	}
	if strings.Compare(p.Key, "") != 0 {
		// start from the position provided
		input.StartAfter = aws.String(p.Key)
	}

	return &SnapshotIterator{
		bucket:          bucket,
		client:          client,
		paginator:       s3.NewListObjectsV2Paginator(client, input),
		maxLastModified: p.Timestamp,
	}, nil
}

// shouldRefreshPage returns a boolean indicating whether the SnapshotIterator is empty or not.
func (w *SnapshotIterator) shouldRefreshPage() bool {
	return w.page == nil || len(w.page.Contents) == w.index
}

// refreshPage retrieves the next page from s3
// returns an error if the end of bucket is reached
func (w *SnapshotIterator) refreshPage(ctx context.Context) error {
	w.page = nil
	w.index = 0
	for w.paginator.HasMorePages() {
		nextPage, err := w.paginator.NextPage(ctx)
		if err != nil {
			return cerrors.Errorf("could not fetch next page: %w", err)
		}
		if len(nextPage.Contents) > 0 {
			w.page = nextPage
			break
		}
	}
	if w.page == nil {
		return ctx.Err()
	}
	return nil
}

// HasNext returns a boolean that indicates whether the iterator has more objects to return or not.
func (w *SnapshotIterator) HasNext(ctx context.Context) bool {
	if w.shouldRefreshPage() {
		err := w.refreshPage(ctx)
		if err != nil {
			return false
		}
	}
	return true
}

// Next returns the next record in the iterator.
// returns an empty record and an error if anything wrong happened.
func (w *SnapshotIterator) Next(ctx context.Context) (sdk.Record, error) {
	if w.shouldRefreshPage() {
		err := w.refreshPage(ctx)
		if err != nil {
			return sdk.Record{}, err
		}
	}

	// after making sure the object is available, get the object's key
	key := w.page.Contents[w.index].Key
	w.index++

	// read object
	object, err := w.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(w.bucket),
		Key:    key,
	})
	if err != nil {
		return sdk.Record{}, cerrors.Errorf("could not fetch the next object: %w", err)
	}

	// check if maxLastModified should be updated
	if w.maxLastModified.Before(*object.LastModified) {
		w.maxLastModified = *object.LastModified
	}

	rawBody, err := ioutil.ReadAll(object.Body)
	if err != nil {
		return sdk.Record{}, cerrors.Errorf("could not read the object's body: %w", err)
	}

	p := position.Position{
		Key:       *key,
		Type:      position.TypeSnapshot,
		Timestamp: w.maxLastModified,
	}

	// create the record
	output := sdk.Record{
		Metadata: map[string]string{
			"content-type": *object.ContentType,
		},
		Position:  p.ToRecordPosition(),
		Payload:   sdk.RawData(rawBody),
		Key:       sdk.RawData(*key),
		CreatedAt: *object.LastModified,
	}

	return output, nil
}
func (w *SnapshotIterator) Stop() {
	// nothing to stop
}
