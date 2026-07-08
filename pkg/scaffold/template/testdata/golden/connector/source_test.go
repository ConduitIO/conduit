package widget_test

import (
	"context"
	"testing"

	widget "github.com/conduitio/conduit-connector-widget"
	"github.com/matryer/is"
)

func TestTeardownSource_NoOpen(t *testing.T) {
	is := is.New(t)
	con := widget.NewSource()
	err := con.Teardown(context.Background())
	is.NoErr(err)
}
