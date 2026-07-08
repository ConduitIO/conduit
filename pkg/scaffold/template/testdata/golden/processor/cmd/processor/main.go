//go:build wasm

package main

import (
	sdk "github.com/conduitio/conduit-processor-sdk"
	widget "github.com/conduitio/conduit-processor-widget"
)

func main() {
	sdk.Run(widget.NewProcessor())
}
