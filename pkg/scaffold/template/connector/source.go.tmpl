package connectorname

import (
	"context"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-connector-sdk"
)

type Source struct {
	sdk.UnimplementedSource

	config           SourceConfig
	lastPositionRead opencdc.Position //nolint:unused // this is just an example
}

type SourceConfig struct {
	sdk.DefaultSourceMiddleware
	// Config includes parameters that are the same in the source and destination.
	Config
	// SourceConfigParam must be provided by the user.
	SourceConfigParam string `json:"sourceConfigParam" validate:"required"`
}

func (s *SourceConfig) Validate(context.Context) error {
	// Custom validation or parsing should be implemented here.
	return nil
}

func NewSource() sdk.Source {
	// Create Source and wrap it in the default middleware.
	return sdk.SourceWithMiddleware(&Source{})
}

func (s *Source) Config() sdk.SourceConfig {
	return &s.config
}

func (s *Source) Open(_ context.Context, _ opencdc.Position) error {
	// Open is called after Configure to signal the plugin it can prepare to
	// start producing records. If needed, the plugin should open connections in
	// this function. The position parameter will contain the position of the
	// last record that was successfully processed, Source should therefore
	// start producing records after this position. The context passed to Open
	// will be cancelled once the plugin receives a stop signal from Conduit.
	return nil
}

func (s *Source) ReadN(context.Context, int) ([]opencdc.Record, error) {
	// ReadN is the same as Read, but returns a batch of records. The connector
	// is expected to return at most n records. If there are fewer records
	// available, it should return all of them. If there are no records available
	// it should block until there are records available or the context is
	// cancelled. If the context is cancelled while ReadN is running, it should
	// return the context error.
	return []opencdc.Record{}, nil
}

func (s *Source) Ack(_ context.Context, _ opencdc.Position) error {
	// Ack signals to the implementation that the record with the supplied
	// position was successfully processed. This method might be called after
	// the context of Read is already cancelled, since there might be
	// outstanding acks that need to be delivered. When Teardown is called it is
	// guaranteed there won't be any more calls to Ack.
	// Ack can be called concurrently with Read.
	return nil
}

func (s *Source) Teardown(_ context.Context) error {
	// Teardown signals to the plugin that there will be no more calls to any
	// other function. After Teardown returns, the plugin should be ready for a
	// graceful shutdown.
	return nil
}
