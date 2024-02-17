package builtin

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/matryer/is"
)

func TestFilter_Process(t *testing.T) {
	is := is.New(t)
	proc := Filter{}
	records := []opencdc.Record{
		{
			Metadata: map[string]string{"key1": "val1"},
			Payload: opencdc.Change{
				After: opencdc.StructuredData{
					"foo": "bar",
				},
			},
		},
		{
			Metadata: map[string]string{"key2": "val2"},
			Payload:  opencdc.Change{},
		},
	}
	want := []sdk.ProcessedRecord{sdk.FilterRecord{}, sdk.FilterRecord{}}
	output := proc.Process(context.Background(), records)
	is.Equal(output, want)
}
