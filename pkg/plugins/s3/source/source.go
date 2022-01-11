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

package source

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/plugins/s3/source/iterator"
	"github.com/conduitio/conduit/pkg/plugins/s3/source/position"
	"github.com/conduitio/conduit/pkg/record"
)

// Source connector
type Source struct {
	config   Config
	iterator Iterator
	client   *s3.Client
}

type Iterator interface {
	HasNext(ctx context.Context) bool
	Next(ctx context.Context) (record.Record, error)
	Stop()
}

// Open parses and initializes the config and makes sure everything is prepared
// to read records.
func (s *Source) Open(ctx context.Context, cfg plugins.Config) error {
	config2, err := Parse(cfg.Settings)
	if err != nil {
		return err
	}

	s.config = config2

	awsCredsProvider := credentials.NewStaticCredentialsProvider(
		config2.AWSAccessKeyID,
		config2.AWSSecretAccessKey,
		"",
	)

	s3Config, err := awsConfig.LoadDefaultConfig(
		ctx,
		awsConfig.WithRegion(config2.AWSRegion),
		awsConfig.WithCredentialsProvider(awsCredsProvider),
	)
	if err != nil {
		return err
	}

	s.client = s3.NewFromConfig(s3Config)

	err = s.bucketExists(ctx, s.config.AWSBucket)
	if err != nil {
		return err
	}

	return nil
}

// Read gets an object from s3 bucket according to the position.
func (s *Source) Read(ctx context.Context, rp record.Position) (record.Record, error) {
	if s.iterator == nil {
		p, err := position.ParseRecordPosition(rp)
		if err != nil {
			return record.Record{}, err
		}

		s.iterator, err = iterator.NewCombinedIterator(s.config.AWSBucket, s.config.PollingPeriod, s.client, p)
		if err != nil {
			return record.Record{}, cerrors.Errorf("couldn't create a combined iterator: %w", err)
		}
	}

	if !s.iterator.HasNext(ctx) {
		return record.Record{}, plugins.ErrEndData
	}
	r, err := s.iterator.Next(ctx)
	if err != nil {
		return record.Record{}, err
	}
	return r, nil
}

func (s *Source) Ack(ctx context.Context, position record.Position) error {
	return nil
}

func (s *Source) Teardown() error {
	if s.iterator != nil {
		s.iterator.Stop()
		s.iterator = nil
	}
	return nil
}

func (s *Source) Validate(cfg plugins.Config) error {
	return nil
}

func (s *Source) bucketExists(ctx context.Context, bucketName string) error {
	// check if the bucket exists
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})
	return err
}
