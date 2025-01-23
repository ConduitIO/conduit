package internal

import (
	"testing"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"github.com/matryer/is"
)

func TestConnectorTypeToString(t *testing.T) {
	is := is.New(t)

	tests := []struct {
		name     string
		connType apiv1.Connector_Type
		want     string
	}{
		{
			name:     "Source",
			connType: apiv1.Connector_TYPE_SOURCE,
			want:     "source",
		},
		{
			name:     "Destination",
			connType: apiv1.Connector_TYPE_DESTINATION,
			want:     "destination",
		},
		{
			name:     "Unspecified",
			connType: apiv1.Connector_TYPE_UNSPECIFIED,
			want:     "unspecified",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is.Equal(ConnectorTypeToString(tt.connType), tt.want)
		})
	}
}
