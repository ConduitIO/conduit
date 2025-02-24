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
	"context"
	"errors"
	"fmt"
	"time"

	cohere "github.com/cohere-ai/cohere-go/v2"
	cohereClient "github.com/cohere-ai/cohere-go/v2/client"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
)

func (p *Processor) processCommandModel(ctx context.Context, records []opencdc.Record) []sdk.ProcessedRecord {
	out := make([]sdk.ProcessedRecord, 0, len(records))
	for _, record := range records {
		for {
			fmt.Println("request:::", string(record.Payload.After.Bytes()))
			resp, err := p.client.V2.Chat(
				ctx,
				&cohere.V2ChatRequest{
					Model: p.config.ModelVersion,
					Messages: cohere.ChatMessages{
						{
							Role: "user",
							User: &cohere.UserMessage{Content: &cohere.UserMessageContent{
								String: string(record.Payload.After.Bytes()),
							}},
						},
					},
				},
				cohereClient.WithToken(p.config.APIKey),
			)
			attempt := p.backoffCfg.Attempt()
			duration := p.backoffCfg.Duration()

			if err != nil {
				switch {
				case errors.As(err, &cohere.GatewayTimeoutError{}),
					errors.As(err, &cohere.InternalServerError{}),
					errors.As(err, &cohere.ServiceUnavailableError{}):

					if attempt < p.config.BackoffRetryCount {
						sdk.Logger(ctx).Debug().Err(err).Float64("attempt", attempt).
							Float64("backoffRetry.count", p.config.BackoffRetryCount).
							Int64("backoffRetry.duration", duration.Milliseconds()).
							Msg("retrying Cohere HTTP request")

						select {
						case <-ctx.Done():
							return append(out, sdk.ErrorRecord{Error: ctx.Err()})
						case <-time.After(duration):
							continue
						}
					} else {
						return append(out, sdk.ErrorRecord{Error: err})
					}

				default:
					// BadRequestError, ClientClosedRequestError, ForbiddenError, InvalidTokenError,
					// NotFoundError, NotImplementedError, TooManyRequestsError, UnauthorizedError, UnprocessableEntityError
					return append(out, sdk.ErrorRecord{Error: err})
				}
			}

			p.backoffCfg.Reset()

			err = p.setField(&record, p.responseBodyRef, resp.String())
			if err != nil {
				return append(out, sdk.ErrorRecord{Error: fmt.Errorf("failed setting response body: %w", err)})
			}
			out = append(out, sdk.SingleRecord(record))
			break

		}
	}
	return out
}

func (p *Processor) setField(r *opencdc.Record, refRes *sdk.ReferenceResolver, data any) error {
	if refRes == nil {
		return nil
	}

	ref, err := refRes.Resolve(r)
	if err != nil {
		return fmt.Errorf("error reference resolver: %w", err)
	}

	err = ref.Set(data)
	if err != nil {
		return fmt.Errorf("error reference set: %w", err)
	}

	return nil
}
