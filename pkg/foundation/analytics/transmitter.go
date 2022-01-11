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

package analytics

import (
	"net/http"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

var _ Analytics = (*Transmitter)(nil)

// Transmitter fulfills Analytics to transmit telemetry and usage data
type Transmitter struct {
	config map[string]string
	client *http.Client
}

// NewTransmitter returns a new Transmitter or an error if one occurred
// Transmitter is used to transmit telemetry and analytics data
func NewTransmitter(cfg map[string]string) (Analytics, error) {
	tr := &http.Transport{
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: false,
	}
	return &Transmitter{
		config: cfg,
		client: &http.Client{Transport: tr},
	}, nil
}

// Send fulfills the Analytics interface for analytics data transmission
func (t *Transmitter) Send(payload Payload) error {
	return cerrors.ErrNotImpl
}
