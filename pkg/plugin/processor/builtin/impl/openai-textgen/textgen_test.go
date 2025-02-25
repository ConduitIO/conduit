package textgen

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/matryer/is"
	"github.com/sashabaranov/go-openai"
)

func TestProcessor_Process(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	processor := newProcessor(ctx, is,
		"You will receive a payload. Your task is to output back the payload in uppercase.")

	recs := testRecords()

	processed := processor.Process(ctx, recs)
	is.Equal(len(processed), 3)

	for i, p := range processed {
		switch p := p.(type) {
		case sdk.SingleRecord:
			is.Equal(string(p.Payload.After.Bytes()), "AFT-REC-"+strconv.Itoa(i+1))
		case sdk.FilterRecord:
			is.Fail() // Filter Record should not happen
		case sdk.ErrorRecord:
			is.Equal("", p.Error.Error())
			is.Fail() // empty error record should not happen
		}
	}
}

func newProcessor(ctx context.Context, is *is.I, devMessage string) sdk.Processor {
	processor := NewProcessor()

	apikey := os.Getenv("OPENAI_API_KEY")
	is.True(apikey != "") // OPENAI_API_KEY must be set

	cfg := config.Config{
		ProcessorConfigModel:            openai.GPT4oMini,
		ProcessorConfigApiKey:           apikey,
		ProcessorConfigDeveloperMessage: devMessage,
		ProcessorConfigTemperature:      "0",
	}

	is.NoErr(processor.Configure(ctx, cfg))

	return processor
}

func testRecords() []opencdc.Record {
	return []opencdc.Record{
		{
			Operation: opencdc.OperationCreate,
			Key:       opencdc.RawData("key1"),
			Payload: opencdc.Change{
				Before: opencdc.RawData("bef-rec-1"),
				After:  opencdc.RawData("aft-rec-1"),
			},
		},
		{
			Operation: opencdc.OperationUpdate,
			Key:       opencdc.RawData("key2"),
			Payload: opencdc.Change{
				Before: opencdc.RawData("bef-rec-2"),
				After:  opencdc.RawData("aft-rec-2"),
			},
		},
		{
			Operation: opencdc.OperationDelete,
			Key:       opencdc.RawData("key3"),
			Payload: opencdc.Change{
				Before: opencdc.RawData("bef-rec-3"),
				After:  opencdc.RawData("aft-rec-3"),
			},
		},
	}
}
