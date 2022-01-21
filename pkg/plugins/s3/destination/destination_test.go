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
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strconv"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins/s3/destination/filevalidator"
	"github.com/conduitio/conduit/pkg/plugins/s3/destination/writer"
)

func TestLocalParquet(t *testing.T) {
	ctx := context.Background()
	destination := &Destination{}

	err := destination.Configure(ctx, map[string]string{
		"aws.access-key-id":     "123",
		"aws.secret-access-key": "secret",
		"aws.region":            "us-west-2",
		"aws.bucket":            "foobucket",
		"buffer-size":           "25",
		"format":                "parquet",
	})
	if err != nil {
		t.Fatalf("failed to parse the Configuration: %v", err)
	}

	err = destination.Open(context.Background())
	if err != nil {
		t.Fatalf("failed to initialize destination: %v", err)
	}

	destination.Writer = &writer.Local{
		Path: "./fixtures",
	}

	for _, record := range generateRecords(50) {
		err = destination.Write(ctx, record, getAckFunc())

		if err != nil {
			t.Fatalf("Write returned an error: %v", err)
		}
	}

	// The code above should produce two files in the fixtures directory:
	// - local-0001.parquet
	// - local-0002.parquet
	// ... that we would compare to two reference files to make sure they're correct.

	validator := &filevalidator.Local{
		Path: "./fixtures",
	}

	err = validateReferences(
		validator,
		"local-0001.parquet", "reference-1.parquet",
		"local-0002.parquet", "reference-2.parquet",
	)

	if err != nil {
		t.Fatalf("comparing references error: %v", err)
	}
}

func TestLocalJSON(t *testing.T) {
	ctx := context.Background()
	destination := &Destination{}

	err := destination.Configure(ctx, map[string]string{
		"aws.access-key-id":     "123",
		"aws.secret-access-key": "secret",
		"aws.region":            "us-west-2",
		"aws.bucket":            "foobucket",
		"buffer-size":           "25",
		"format":                "json",
	})
	if err != nil {
		t.Fatalf("failed to parse the Configuration: %v", err)
	}

	err = destination.Open(context.Background())
	if err != nil {
		t.Fatalf("failed to initialize destination: %v", err)
	}

	destination.Writer = &writer.Local{
		Path: "./fixtures",
	}

	for _, record := range generateRecords(50) {
		err = destination.Write(ctx, record, getAckFunc())

		if err != nil {
			t.Fatalf("Write returned an error: %v", err)
		}
	}

	// The code above should produce two files in the fixtures directory:
	// - local-0001.json
	// - local-0002.json
	// ... that we would compare to two reference files to make sure they're correct.

	validator := &filevalidator.Local{
		Path: "./fixtures",
	}

	err = validateReferences(
		validator,
		"local-0001.json", "reference-1.json",
		"local-0002.json", "reference-2.json",
	)

	if err != nil {
		t.Fatalf("comparing references error: %v", err)
	}
}

func TestS3Parquet(t *testing.T) {
	ctx := context.Background()
	awsAccessKeyID := os.Getenv("AWS_ACCESS_KEY_ID")

	if awsAccessKeyID == "" {
		t.Skip("AWS_ACCESS_KEY_ID env var must be set")
	}

	awsSecretAccessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if awsSecretAccessKey == "" {
		t.Skip("AWS_SECRET_ACCESS_KEY env var must be set")
	}

	awsBucketName := os.Getenv("AWS_S3_BUCKET")

	if awsBucketName == "" {
		t.Skip("AWS_S3_BUCKET env var must be set")
	}

	awsRegion := os.Getenv("AWS_REGION")

	if awsRegion == "" {
		t.Skip("AWS_REGION env var must be set")
	}

	awsSessionToken := os.Getenv("AWS_SESSION_TOKEN")

	destination := &Destination{}

	err := destination.Configure(ctx, map[string]string{
		"aws.access-key-id":     awsAccessKeyID,
		"aws.secret-access-key": awsSecretAccessKey,
		"aws.session-token":     awsSessionToken,
		"aws.region":            awsRegion,
		"aws.bucket":            awsBucketName,
		"buffer-size":           "25",
		"format":                "parquet",
		"prefix":                "test",
	})
	if err != nil {
		t.Fatalf("failed to parse the Configuration: %v", err)
	}

	err = destination.Open(context.Background())
	if err != nil {
		t.Fatalf("failed to initialize destination: %v", err)
	}

	for _, record := range generateRecords(50) {
		err = destination.Write(ctx, record, getAckFunc())

		if err != nil {
			t.Fatalf("Write returned an error: %v", err)
		}
	}

	writer, ok := destination.Writer.(*writer.S3)

	if !ok {
		t.Fatalf("Destination writer expected to be writer.S3, but is actually %+v", writer)
	}

	if len(writer.FilesWritten) != 2 {
		t.Fatalf("Expected writer to have written 2 files, got %d", len(writer.FilesWritten))
	}

	validator := &filevalidator.S3{
		AccessKeyID:     awsAccessKeyID,
		SecretAccessKey: awsSecretAccessKey,
		SessionToken:    awsSessionToken,
		Bucket:          awsBucketName,
		Region:          awsRegion,
	}

	err = validateReferences(
		validator,
		writer.FilesWritten[0], "reference-1.parquet",
		writer.FilesWritten[1], "reference-2.parquet",
	)

	if err != nil {
		t.Fatalf("comparing references error: %v", err)
	}
}

func generateRecords(count int) []sdk.Record {
	var result []sdk.Record

	for i := 0; i < count; i++ {
		result = append(result, sdk.Record{
			Position:  []byte(strconv.Itoa(i)),
			Payload:   sdk.RawData(fmt.Sprintf("this is a message #%d", i+1)),
			Key:       sdk.RawData(fmt.Sprintf("key-%d", i)),
			Metadata:  map[string]string{"number": fmt.Sprint(i)},
			CreatedAt: time.Date(2020, 1, 1, 1, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second),
		})
	}

	return result
}

func validateReferences(validator filevalidator.FileValidator, paths ...string) error {
	for i := 0; i < len(paths); i += 2 {
		fileName := paths[i]
		referencePath := paths[i+1]
		reference, err := ioutil.ReadFile(path.Join("./fixtures", referencePath))

		if err != nil {
			return err
		}

		err = validator.Validate(fileName, reference)

		if err != nil {
			return err
		}
	}

	return nil
}

func getAckFunc() sdk.AckFunc {
	return func(ackErr error) error { return nil }
}
