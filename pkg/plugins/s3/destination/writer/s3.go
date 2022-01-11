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

package writer

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/conduitio/conduit/pkg/record"
)

// S3FilesWrittenLength defines the number of last filenames an S3 Writer keep
// track of when storing files in S3. This is only used in tests.
const S3FilesWrittenLength = 100

// S3 writer stores batch bytes into an S3 bucket as a file.
type S3 struct {
	KeyPrefix    string
	Bucket       string
	Position     record.Position
	Error        error
	FilesWritten []string
	Client       *s3.Client
}

var _ Writer = (*S3)(nil)

// S3Config is a type used to initialize an S3 Writer
type S3Config struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string
	Bucket          string
	KeyPrefix       string
}

// NewS3 takes an S3Config reference and produces an S3 Writer
func NewS3(ctx context.Context, cfg *S3Config) (*S3, error) {
	awsCredsProvider := credentials.NewStaticCredentialsProvider(
		cfg.AccessKeyID,
		cfg.SecretAccessKey,
		cfg.SessionToken,
	)

	awsConfig, err := config.LoadDefaultConfig(
		ctx,
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(awsCredsProvider),
	)

	if err != nil {
		return nil, err
	}

	return &S3{
		Bucket:       cfg.Bucket,
		KeyPrefix:    cfg.KeyPrefix,
		FilesWritten: make([]string, 0, S3FilesWrittenLength),
		Client:       s3.NewFromConfig(awsConfig),
	}, nil
}

// Write stores the batch on AWS S3 as a file
func (w *S3) Write(ctx context.Context, batch *Batch) error {
	batchBytes, err := batch.Bytes()

	if err != nil {
		return err
	}

	key := fmt.Sprintf(
		"%d.%s",
		time.Now().UnixNano(),
		batch.Format.Ext(),
	)

	if w.KeyPrefix != "" {
		key = path.Join(w.KeyPrefix, key)
	}

	_, err = w.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(w.Bucket),
		Key:                  aws.String(key),
		ACL:                  types.ObjectCannedACLPrivate, // TODO: config?
		Body:                 bytes.NewReader(batchBytes),
		ContentLength:        int64(len(batchBytes)),
		ContentType:          aws.String(batch.Format.MimeType()),
		ContentDisposition:   aws.String("attachment"),
		ServerSideEncryption: types.ServerSideEncryptionAes256, // TODO: config?
	})

	if err != nil {
		return err
	}

	// Log written file names in here so we could access those files in tests.
	// Also, truncate to last 100 elements to prevent memory leaks in
	// production.
	w.FilesWritten = append(w.FilesWritten, key)
	if len(w.FilesWritten) > S3FilesWrittenLength {
		w.FilesWritten = w.FilesWritten[len(w.FilesWritten)-S3FilesWrittenLength:]
	}

	w.Position = batch.LastPosition()

	return nil
}

// LastPosition returns the last persisted position
func (w *S3) LastPosition() record.Position {
	return w.Position
}
