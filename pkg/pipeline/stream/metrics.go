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

package stream

import (
	"context"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/foundation/metrics"
)

type MetricsNode struct {
	Name           string
	BytesHistogram metrics.Histogram

	base   pubSubNodeBase
	logger log.CtxLogger
}

func (n *MetricsNode) ID() string {
	return n.Name
}

func (n *MetricsNode) Run(ctx context.Context) error {
	trigger, cleanup, err := n.base.Trigger(ctx, n.logger, nil)
	if err != nil {
		return err
	}

	defer cleanup()
	for {
		msg, err := trigger()
		if err != nil || msg == nil {
			return err
		}

		msg.RegisterAckHandler(func(msg *Message) error {
			// TODO for now we call method Bytes() on key and payload to get the
			//  bytes representation. In case of a structured payload or key it
			//  is marshaled into JSON, which might not be the correct way to
			//  determine bytes. Not sure how we could improve this part without
			//  offloading the bytes calculation to the plugin.
			var bytes int
			if msg.Record.Key != nil {
				bytes += len(msg.Record.Key.Bytes())
			}
			if msg.Record.Payload.Before != nil {
				bytes += len(msg.Record.Payload.Before.Bytes())
			}
			if msg.Record.Payload.After != nil {
				bytes += len(msg.Record.Payload.After.Bytes())
			}
			n.BytesHistogram.Observe(float64(bytes))
			return nil
		})

		err = n.base.Send(ctx, n.logger, msg)
		if err != nil {
			return msg.Nack(err, n.ID())
		}
	}
}

func (n *MetricsNode) Sub(in <-chan *Message) {
	n.base.Sub(in)
}

func (n *MetricsNode) Pub() <-chan *Message {
	return n.base.Pub()
}

func (n *MetricsNode) SetLogger(logger log.CtxLogger) {
	n.logger = logger
}
