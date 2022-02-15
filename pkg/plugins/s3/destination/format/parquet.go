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

package format

import (
	"io/ioutil"
	"os"

	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/xitongsys/parquet-go-source/local"
	"github.com/xitongsys/parquet-go/parquet"
	"github.com/xitongsys/parquet-go/writer"
)

type parquetRecord struct {
	// TODO save schema type
	Position  string            `parquet:"name=position, type=BYTE_ARRAY"`
	Payload   string            `parquet:"name=payload, type=BYTE_ARRAY"`
	Key       string            `parquet:"name=key, type=BYTE_ARRAY"`
	Metadata  map[string]string `parquet:"name=metadata, type=MAP, convertedtype=MAP, keytype=BYTE_ARRAY, keyconvertedtype=UTF8, valuetype=BYTE_ARRAY, valueconvertedtype=UTF8"`
	Timestamp int64             `parquet:"name=timestamp, type=INT64, convertedtype=TIME_MICROS"`
}

func makeParquetBytes(records []sdk.Record) ([]byte, error) {
	var err error

	// TODO: make this less dumb

	// Lol we literally open a tmpfile just for a name.
	tmpFile, err := ioutil.TempFile(os.TempDir(), "s3destination-parquet-")

	if err != nil {
		return nil, err
	}

	err = tmpFile.Close()

	if err != nil {
		return nil, err
	}

	fw, err := local.NewLocalFileWriter(tmpFile.Name())

	if err != nil {
		return nil, err
	}

	pw, err := writer.NewParquetWriter(fw, new(parquetRecord), int64(len(records)))

	if err != nil {
		return nil, err
	}

	pw.CompressionType = parquet.CompressionCodec_GZIP

	for _, r := range records {
		pr := &parquetRecord{
			Position:  string(r.Position),
			Payload:   string(r.Payload.Bytes()),
			Key:       string(r.Key.Bytes()),
			Metadata:  r.Metadata,
			Timestamp: r.CreatedAt.UnixNano(),
		}

		if err = pw.Write(pr); err != nil {
			return nil, err
		}
	}

	if err = pw.WriteStop(); err != nil {
		return nil, err
	}

	err = fw.Close()

	if err != nil {
		return nil, err
	}

	bytes, err := ioutil.ReadFile(tmpFile.Name())

	if err != nil {
		return nil, err
	}

	err = os.Remove(tmpFile.Name())

	if err != nil {
		return nil, err
	}

	return bytes, nil
}
