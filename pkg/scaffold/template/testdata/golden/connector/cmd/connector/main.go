package main

import (
	widget "github.com/conduitio/conduit-connector-widget"
	sdk "github.com/conduitio/conduit-connector-sdk"
)

func main() {
	sdk.Serve(widget.Connector)
}
