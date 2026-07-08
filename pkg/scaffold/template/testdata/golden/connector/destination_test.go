package widget_test

import (
	"context"
	"testing"

	widget "github.com/conduitio/conduit-connector-widget"
	"github.com/matryer/is"
)

func TestTeardown_NoOpen(t *testing.T) {
	is := is.New(t)
	con := widget.NewDestination()
	err := con.Teardown(context.Background())
	is.NoErr(err)
}
