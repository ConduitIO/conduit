package destination

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/jackc/pgx/v4"
)

type Adapter struct {
	sdk.UnimplementedDestination

	config config

	conn *pgx.Conn
}

// config represents the internal configuration of the adapter.
type config struct {
	url       string
	tableName string
}

func New() sdk.Destination {
	return &Adapter{}
}

// Configure ...
func (d *Adapter) Configure(ctx context.Context, cfg map[string]string) error {
	d.config = config{
		url:       cfg["url"],
		tableName: cfg["table"],
	}
	return nil
}

// Open ...
func (d *Adapter) Open(ctx context.Context) error {
	if err := d.connect(ctx, d.config.url); err != nil {
		return cerrors.Errorf("failed to connecto to postgres: %w", err)
	}
	return nil
}

// Write ...
func (d *Adapter) Write(ctx context.Context, record sdk.Record) error {
	return nil
}

// Flush ...
func (d *Adapter) Flush(context.Context) error {
	return nil
}

// Teardown ...
func (d *Adapter) Teardown(ctx context.Context) error {
	return nil
}

// connect connects the Adapter to postgres or returns an error
func (d *Adapter) connect(ctx context.Context, uri string) error {
	conn, err := pgx.Connect(ctx, uri)
	if err != nil {
		return cerrors.Errorf("failed to open connection: %w", err)
	}
	d.conn = conn
	return nil
}
