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
	Name      string
	Histogram metrics.RecordBytesHistogram

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

		if msg.filtered {
			n.logger.Trace(ctx).Str(log.MessageIDField, msg.ID()).
				Msg("message marked as filtered, sending directly to next node")
			err = n.base.Send(ctx, n.logger, msg)
			if err != nil {
				return msg.Nack(err, n.ID())
			}
			continue
		}

		msg.RegisterAckHandler(func(msg *Message) error {
			n.Histogram.Observe(msg.Record)
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
