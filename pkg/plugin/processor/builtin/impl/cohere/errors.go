// Copyright © 2025 Meroxa, Inc.
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

package cohere

import (
	"fmt"

	cohere "github.com/cohere-ai/cohere-go/v2"
	"github.com/cohere-ai/cohere-go/v2/core"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// isRetryable reports whether err is a server-side failure that is worth
// retrying. The SDK returns its error types as pointers, so the targets are
// pointers too: a value target never matches and the retry would never run.
func isRetryable(err error) bool {
	var (
		timeout     *cohere.GatewayTimeoutError
		internal    *cohere.InternalServerError
		unavailable *cohere.ServiceUnavailableError
	)
	return cerrors.As(err, &timeout) || cerrors.As(err, &internal) || cerrors.As(err, &unavailable)
}

// apiError is a failed Cohere request without what the API answered. The
// response body can repeat the request, and so the record, so only the HTTP
// status and the SDK error type are reported. The original error stays in the
// chain for errors.As.
type apiError struct {
	status int
	kind   string
	err    error
}

func (e *apiError) Error() string {
	return fmt.Sprintf("cohere request failed: HTTP status %d (%s)", e.status, e.kind)
}
func (e *apiError) Unwrap() error { return e.err }

// sanitizeAPIError replaces an SDK API error by an apiError. Other errors
// (a network failure, a context error) are returned as they are.
func sanitizeAPIError(err error) error {
	var api *core.APIError
	if err == nil || !cerrors.As(err, &api) {
		return err
	}
	return &apiError{status: api.StatusCode, kind: fmt.Sprintf("%T", err), err: err}
}
