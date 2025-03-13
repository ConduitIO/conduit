// Copyright © 2024 Meroxa, Inc.
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

package openaiwrap

import (
	"context"
	"fmt"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/jpillora/backoff"
	"github.com/sashabaranov/go-openai"
)

// Config contains common configuration options for OpenAI processors
type Config struct {
	// MaxRetries is the maximum number of retries for API calls. Defaults to 3.
	MaxRetries int `json:"max_retries" default:"3"`
	// InitialBackoff is the initial backoff duration in milliseconds. Defaults to 1000ms (1s).
	InitialBackoff int `json:"initial_backoff" default:"1000"`
	// MaxBackoff is the maximum backoff duration in milliseconds. Defaults to 30000ms (30s).
	MaxBackoff int `json:"max_backoff" default:"30000"`
	// BackoffFactor is the factor by which the backoff increases. Defaults to 2.0
	BackoffFactor float64 `json:"backoff_factor" default:"2.0"`
}

type OpenaiCaller[T any] interface {
	Call(ctx context.Context, input string) (T, error)
}

// CallWithRetry handles retrying API calls with exponential backoff. This is
// meant to be used with openai api calls.
func CallWithRetry[T any](ctx context.Context, config Config, caller OpenaiCaller[T], payload string) (T, error) {
	b := &backoff.Backoff{
		Min:    time.Duration(config.InitialBackoff) * time.Millisecond,
		Max:    time.Duration(config.MaxBackoff) * time.Millisecond,
		Factor: config.BackoffFactor,
		Jitter: true,
	}

	var result T
	var err error
	var attempt int

	logger := sdk.Logger(ctx)

	for {
		attempt++

		if attempt > config.MaxRetries+1 {
			return result, fmt.Errorf("exceeded maximum retries (%d): %w", config.MaxRetries, err)
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		result, err = caller.Call(ctx, payload)
		if err == nil {
			if attempt > 1 {
				logger.Debug().Int("attempts", attempt).Msg("OpenAI API call succeeded after retries")
			}
			break
		}

		if !isRetryableError(err) {
			return result, fmt.Errorf("OpenAI API call failed with non-retryable error: %w", err)
		}

		backoffDuration := b.Duration()
		logger.Warn().
			Int("attempt", attempt).
			Dur("backoff", backoffDuration).
			Err(err).
			Msg("OpenAI API call failed, retrying after backoff")

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(backoffDuration):
		}
	}

	return result, nil
}

func ProcessRecordField[T any](
	ctx context.Context,
	rec opencdc.Record,
	resolver sdk.ReferenceResolver,
	processor func(ctx context.Context, input string) (T, error),
	formatter func(T) ([]byte, error),
) (opencdc.Record, error) {
	logger := sdk.Logger(ctx)

	ref, err := resolver.Resolve(&rec)
	if err != nil {
		return rec, fmt.Errorf("failed to resolve reference: %w", err)
	}

	val := ref.Get()

	var payload string
	switch v := val.(type) {
	case opencdc.Position:
		payload = string(v)
		result, err := processor(ctx, payload)
		if err != nil {
			return rec, err
		}

		formatted, err := formatter(result)
		if err != nil {
			return rec, err
		}

		logger.Trace().Msgf("processed record position")
		if err := ref.Set(opencdc.Position(formatted)); err != nil {
			return rec, fmt.Errorf("failed to set position: %w", err)
		}

	case opencdc.Data:
		payload = string(v.Bytes())
		result, err := processor(ctx, payload)
		if err != nil {
			return rec, err
		}

		formatted, err := formatter(result)
		if err != nil {
			return rec, err
		}

		logger.Trace().Msgf("processed record data")
		var data opencdc.Data = opencdc.RawData(formatted)
		if err := ref.Set(data); err != nil {
			return rec, fmt.Errorf("failed to set data: %w", err)
		}

	case string:
		payload = v
		result, err := processor(ctx, payload)
		if err != nil {
			return rec, err
		}

		formatted, err := formatter(result)
		if err != nil {
			return rec, err
		}

		logger.Trace().Msgf("processed record string")
		if err := ref.Set(string(formatted)); err != nil {
			return rec, fmt.Errorf("failed to set data: %w", err)
		}

	default:
		return rec, fmt.Errorf("unsupported type %T", v)
	}

	return rec, nil
}

// isRetryableError determines if an error from the OpenAI API is retryable
func isRetryableError(err error) bool {
	var apiErr *openai.APIError
	if !cerrors.As(err, &apiErr) {
		return true
	}

	switch apiErr.HTTPStatusCode {
	case 429:
		return true
	case 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
