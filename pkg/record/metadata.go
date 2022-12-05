// Copyright © 2022 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package record

import (
	"fmt"
	"strconv"
	"time"
)

const (
	// OpenCDCVersion is a constant that should be used as the value in the
	// metadata field MetadataVersion. It ensures the OpenCDC format version can
	// be easily identified in case the record gets marshaled into a different
	// untyped format (e.g. JSON).
	OpenCDCVersion = "v1"

	// MetadataOpenCDCVersion is a Record.Metadata key for the version of the
	// OpenCDC format (e.g. "v1"). This field exists to ensure the OpenCDC
	// format version can be easily identified in case the record gets marshaled
	// into a different untyped format (e.g. JSON).
	MetadataOpenCDCVersion = "opencdc.version"
	// MetadataCreatedAt is a Record.Metadata key for the time when the record
	// was created in the 3rd party system. The expected format is a unix
	// timestamp in nanoseconds.
	MetadataCreatedAt = "opencdc.createdAt"
	// MetadataReadAt is a Record.Metadata key for the time when the record was
	// read from the 3rd party system. The expected format is a unix timestamp
	// in nanoseconds.
	MetadataReadAt = "opencdc.readAt"

	// MetadataConduitSourcePluginName is a Record.Metadata key for the name of
	// the source plugin that created this record.
	MetadataConduitSourcePluginName = "conduit.source.plugin.name"
	// MetadataConduitSourcePluginVersion is a Record.Metadata key for the
	// version of the source plugin that created this record.
	MetadataConduitSourcePluginVersion = "conduit.source.plugin.version"
	// MetadataConduitDestinationPluginName is a Record.Metadata key for the
	// name of the destination plugin that has written this record
	// (only available in records once they are written by a destination).
	MetadataConduitDestinationPluginName = "conduit.destination.plugin.name"
	// MetadataConduitDestinationPluginVersion is a Record.Metadata key for the
	// version of the destination plugin that has written this record
	// (only available in records once they are written by a destination).
	MetadataConduitDestinationPluginVersion = "conduit.destination.plugin.version"

	// MetadataConduitSourceConnectorID is a Record.Metadata key for the ID of
	// the source connector that received this record.
	MetadataConduitSourceConnectorID = "conduit.source.connector.id"
	// MetadataConduitDLQNackError is a Record.Metadata key for the error that
	// caused a record to be nacked and pushed to the dead-letter queue.
	MetadataConduitDLQNackError = "conduit.dlq.nack.error"
	// MetadataConduitDLQNackNodeID is a Record.Metadata key for the ID of the
	// internal node that nacked the record.
	MetadataConduitDLQNackNodeID = "conduit.dlq.nack.node.id"
)

// SetOpenCDCVersion sets the metadata value for key MetadataVersion to the
// current version of OpenCDC used.
func (m Metadata) SetOpenCDCVersion() {
	m[MetadataOpenCDCVersion] = OpenCDCVersion
}

// GetOpenCDCVersion returns the value for key
// MetadataOpenCDCVersion. If the value does not exist or is empty the
// function returns ErrMetadataFieldNotFound.
func (m Metadata) GetOpenCDCVersion() (string, error) {
	return m.getValue(MetadataOpenCDCVersion)
}

// GetCreatedAt parses the value for key MetadataCreatedAt as a unix
// timestamp. If the value does not exist or the value is empty the function
// returns ErrMetadataFieldNotFound. If the value is not a valid unix timestamp
// in nanoseconds the function returns an error.
func (m Metadata) GetCreatedAt() (time.Time, error) {
	raw, err := m.getValue(MetadataCreatedAt)
	if err != nil {
		return time.Time{}, err
	}

	unixNano, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse value for %q: %w", MetadataCreatedAt, err)
	}

	return time.Unix(0, unixNano), nil
}

// SetCreatedAt sets the metadata value for key MetadataCreatedAt as a
// unix timestamp in nanoseconds.
func (m Metadata) SetCreatedAt(createdAt time.Time) {
	m[MetadataCreatedAt] = strconv.FormatInt(createdAt.UnixNano(), 10)
}

// GetReadAt parses the value for key MetadataReadAt as a unix
// timestamp. If the value does not exist or the value is empty the function
// returns ErrMetadataFieldNotFound. If the value is not a valid unix timestamp
// in nanoseconds the function returns an error.
func (m Metadata) GetReadAt() (time.Time, error) {
	raw, err := m.getValue(MetadataReadAt)
	if err != nil {
		return time.Time{}, err
	}

	unixNano, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse value for %q: %w", MetadataReadAt, err)
	}

	return time.Unix(0, unixNano), nil
}

// SetReadAt sets the metadata value for key MetadataReadAt as a unix
// timestamp in nanoseconds.
func (m Metadata) SetReadAt(createdAt time.Time) {
	m[MetadataReadAt] = strconv.FormatInt(createdAt.UnixNano(), 10)
}

// GetConduitSourcePluginName returns the value for key
// MetadataConduitSourcePluginName. If the value does not exist or is empty the
// function returns ErrMetadataFieldNotFound.
func (m Metadata) GetConduitSourcePluginName() (string, error) {
	return m.getValue(MetadataConduitSourcePluginName)
}

// SetConduitSourcePluginName sets the metadata value for key
// MetadataConduitSourcePluginName.
func (m Metadata) SetConduitSourcePluginName(name string) {
	m[MetadataConduitSourcePluginName] = name
}

// GetConduitSourcePluginVersion returns the value for key
// MetadataConduitSourcePluginVersion. If the value does not exist or is empty
// the function returns ErrMetadataFieldNotFound.
func (m Metadata) GetConduitSourcePluginVersion() (string, error) {
	return m.getValue(MetadataConduitSourcePluginVersion)
}

// SetConduitSourcePluginVersion sets the metadata value for key
// MetadataConduitSourcePluginVersion.
func (m Metadata) SetConduitSourcePluginVersion(version string) {
	m[MetadataConduitSourcePluginVersion] = version
}

// GetConduitDestinationPluginName returns the value for key
// MetadataConduitDestinationPluginName. If the value does not exist or is empty
// the function returns ErrMetadataFieldNotFound.
func (m Metadata) GetConduitDestinationPluginName() (string, error) {
	return m.getValue(MetadataConduitDestinationPluginName)
}

// SetConduitDestinationPluginName sets the metadata value for key
// MetadataConduitDestinationPluginName.
func (m Metadata) SetConduitDestinationPluginName(name string) {
	m[MetadataConduitDestinationPluginName] = name
}

// GetConduitDestinationPluginVersion returns the value for key
// MetadataConduitDestinationPluginVersion. If the value does not exist or is
// empty the function returns ErrMetadataFieldNotFound.
func (m Metadata) GetConduitDestinationPluginVersion() (string, error) {
	return m.getValue(MetadataConduitDestinationPluginVersion)
}

// SetConduitDestinationPluginVersion sets the metadata value for key
// MetadataConduitDestinationPluginVersion.
func (m Metadata) SetConduitDestinationPluginVersion(version string) {
	m[MetadataConduitDestinationPluginVersion] = version
}

// GetConduitSourceConnectorID returns the value for key
// MetadataConduitSourceConnectorID. If the value does not exist or is empty the
// function returns ErrMetadataFieldNotFound.
func (m Metadata) GetConduitSourceConnectorID() (string, error) {
	return m.getValue(MetadataConduitSourceConnectorID)
}

// SetConduitSourceConnectorID sets the metadata value for key
// MetadataConduitSourceConnectorID.
func (m Metadata) SetConduitSourceConnectorID(id string) {
	m[MetadataConduitSourceConnectorID] = id
}

// GetConduitDLQNackError returns the value for key
// MetadataConduitDLQNackError. If the value does not exist or is empty the
// function returns ErrMetadataFieldNotFound.
func (m Metadata) GetConduitDLQNackError() (string, error) {
	return m.getValue(MetadataConduitDLQNackError)
}

// SetConduitDLQNackError sets the metadata value for key
// MetadataConduitDLQNackError.
func (m Metadata) SetConduitDLQNackError(err string) {
	m[MetadataConduitDLQNackError] = err
}

// GetConduitDLQNackNodeID returns the value for key
// MetadataConduitDLQNackNodeID. If the value does not exist or is empty the
// function returns ErrMetadataFieldNotFound.
func (m Metadata) GetConduitDLQNackNodeID() (string, error) {
	return m.getValue(MetadataConduitDLQNackNodeID)
}

// SetConduitDLQNackNodeID sets the metadata value for key
// MetadataConduitDLQNackNodeID.
func (m Metadata) SetConduitDLQNackNodeID(id string) {
	m[MetadataConduitDLQNackNodeID] = id
}

// getValue returns the value for a specific key. If the value does not exist or
// is empty the function returns ErrMetadataFieldNotFound.
func (m Metadata) getValue(key string) (string, error) {
	str := m[key]
	if str == "" {
		return "", fmt.Errorf("failed to get value for %q: %w", key, ErrMetadataFieldNotFound)
	}
	return str, nil
}
