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

package mock

import (
	"encoding/json"

	"github.com/conduitio/conduit/pkg/connector"
)

type jsonConnector struct {
	ID     string
	Config connector.Config
}

func (m *Source) MarshalJSON() ([]byte, error) {
	conn := jsonConnector{ID: m.ID(), Config: m.Config()}
	return json.Marshal(conn)
}

func (m *Source) UnmarshalJSON(b []byte) error {
	var conn jsonConnector
	err := json.Unmarshal(b, &conn)
	if err != nil {
		return err
	}
	m.EXPECT().ID().Return(conn.ID).AnyTimes()
	m.EXPECT().Config().Return(conn.Config).AnyTimes()
	return nil
}

func (m *Destination) MarshalJSON() ([]byte, error) {
	conn := jsonConnector{ID: m.ID(), Config: m.Config()}
	return json.Marshal(conn)
}

func (m *Destination) UnmarshalJSON(b []byte) error {
	var conn jsonConnector
	err := json.Unmarshal(b, &conn)
	if err != nil {
		return err
	}
	m.EXPECT().ID().Return(conn.ID).AnyTimes()
	m.EXPECT().Config().Return(conn.Config).AnyTimes()
	return nil
}
