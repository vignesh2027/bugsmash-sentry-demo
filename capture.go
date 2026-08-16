package main

import (
	"context"
	"errors"

	"github.com/getsentry/sentry-go"
	redis "github.com/redis/go-redis/v9"
)

// captureRedisFailure reports a failed Redis command to Sentry.
//
// The capture runs on a scope carrying the command, the key and which hook
// attachment strategy was in play, so the resulting Issue says what happened
// rather than just that something did. The context is the one holding the
// active OpenTelemetry span, which is what lets the OTel integration link the
// Issue back to the trace it belongs to.
//
// redis.Nil means the key was absent, which is an ordinary result rather than a
// failure, so it is not reported.
func captureRedisFailure(ctx context.Context, mode, op, key string, err error) {
	if err == nil || errors.Is(err, redis.Nil) {
		return
	}

	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		hub = sentry.CurrentHub()
	}

	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("redis.op", op)
		scope.SetTag("hook.mode", mode)
		scope.SetContext("redis command", map[string]any{
			"operation": op,
			"key":       key,
			// Recorded so the Issue itself carries the finding: under the buggy
			// attachment this command produced no span, so an operator looking
			// at the trace would have seen nothing at all.
			"traced": mode == "fixed",
		})
		hub.CaptureException(err)
	})
}
