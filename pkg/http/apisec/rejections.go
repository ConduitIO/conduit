// Copyright © 2026 Meroxa, Inc.
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

package apisec

import (
	"context"
	"sync"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	promclient "github.com/prometheus/client_golang/prometheus"
)

// rejectionWarnInterval is the shortest gap between two WARN lines for the
// same rejection code. A guessing attack still shows up (in the first line, in
// the "suppressed" count on the next and in the counter), without letting an
// attacker turn the log into the denial of service.
const rejectionWarnInterval = 10 * time.Second

// RejectionsMetricName is the counter of requests refused by this package.
// Its only label is "code", one of the registered api.* reasons, so the series
// set is bounded and carries no request data.
const RejectionsMetricName = "conduit_api_rejected_requests_total"

// rejectionCodes are the reasons a request can be refused with. All of them are
// initialized to zero so a scrape shows the series before the first attack.
var rejectionCodes = []conduiterr.Code{
	CodeUnauthenticated, CodeOriginForbidden, CodeHostForbidden, CodeContentTypeUnsupported,
}

// Rejections counts refused requests per code and logs them, rate limited per
// code. The zero value and a nil *Rejections are usable and do nothing.
type Rejections struct {
	counter *promclient.CounterVec
	logger  log.CtxLogger
	now     func() time.Time

	mu         sync.Mutex
	lastWarn   map[string]time.Time
	suppressed map[string]int
}

// NewRejections registers the counter on reg (nil skips metrics) and logs to
// logger. Registration never fails the caller: an already registered counter
// with the same shape is reused (several runtimes in one process), and any
// other error leaves the counter unexported rather than panicking.
func NewRejections(reg promclient.Registerer, logger log.CtxLogger) *Rejections {
	r := &Rejections{
		logger:     logger.WithComponent("apisec.Rejections"),
		now:        time.Now,
		lastWarn:   map[string]time.Time{},
		suppressed: map[string]int{},
	}
	cv := promclient.NewCounterVec(promclient.CounterOpts{
		Name: RejectionsMetricName,
		Help: "Requests refused by the API access controls, by stable error code.",
	}, []string{"code"})
	if reg != nil {
		if err := reg.Register(cv); err != nil {
			var are promclient.AlreadyRegisteredError
			if cerrors.As(err, &are) {
				if existing, ok := are.ExistingCollector.(*promclient.CounterVec); ok {
					cv = existing
				}
			}
		}
	}
	for _, c := range rejectionCodes {
		cv.WithLabelValues(c.Reason())
	}
	r.counter = cv
	return r
}

// Record counts one refusal with code and logs it at WARN, at most once per
// rejectionWarnInterval per code. msg must not contain request data that an
// attacker controls beyond the fields passed here, and never a credential.
func (r *Rejections) Record(ctx context.Context, code conduiterr.Code, remoteAddr, msg string) {
	if r == nil {
		return
	}
	reason := code.Reason()
	if r.counter != nil {
		r.counter.WithLabelValues(reason).Inc()
	}

	r.mu.Lock()
	now := r.now()
	if last, ok := r.lastWarn[reason]; ok && now.Sub(last) < rejectionWarnInterval {
		r.suppressed[reason]++
		r.mu.Unlock()
		return
	}
	r.lastWarn[reason] = now
	suppressed := r.suppressed[reason]
	r.suppressed[reason] = 0
	r.mu.Unlock()

	r.logger.Warn(ctx).Str("code", reason).Str("remote_addr", remoteAddr).Int("suppressed_since_last", suppressed).Msg(msg)
}
