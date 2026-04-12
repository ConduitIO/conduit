package kafka

import (
	connector "github.com/conduitio/conduit-connector-kafka"
	sdk "github.com/conduitio/conduit-connector-sdk"
)

// Connector combines the actual connector implementation with the version constant.
var Connector sdk.Connector = sdk.NewConnectorWithVersion(connector.Connector, Version)
