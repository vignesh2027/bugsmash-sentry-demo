package main

import (
	"context"
	"net"

	redis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// otelRedisHook mirrors the hook that opentelemetry-go-compile-instrumentation
// attaches to a go-redis client. It opens a client span before the command runs
// and closes it afterwards, recording the endpoint it was talking to.
//
// The span is started before the command is handed to the next hook in the
// chain, so a command that never reaches a server still produces a span. That
// is what lets this demo run without a Redis server.
type otelRedisHook struct {
	addr string
}

func newOtelRedisHook(addr string) redis.Hook {
	return &otelRedisHook{addr: addr}
}

func (h *otelRedisHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *otelRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		host, port := splitHostPort(h.addr)

		ctx, span := otel.Tracer(tracerName).Start(ctx, "redis."+cmd.Name(),
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "redis"),
				attribute.String("db.operation", cmd.Name()),
				attribute.String("server.address", host),
				attribute.String("server.port", port),
				// Carried so the Sentry trace makes it obvious which shard
				// each span came from.
				attribute.String("redis.shard", h.addr),
			),
		)
		defer span.End()

		err := next(ctx, cmd)
		if err != nil && err != redis.Nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	}
}

func (h *otelRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		ctx, span := otel.Tracer(tracerName).Start(ctx, "redis.pipeline",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system", "redis"),
				attribute.String("redis.shard", h.addr),
				attribute.Int("db.redis.num_cmd", len(cmds)),
			),
		)
		defer span.End()

		err := next(ctx, cmds)
		if err != nil && err != redis.Nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	}
}

func splitHostPort(addr string) (string, string) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, ""
	}
	return host, port
}

// ---------------------------------------------------------------------------
// The bug, and the fix.
// ---------------------------------------------------------------------------

// attachRingHooksBuggy reproduces the original afterNewRingClientV9.
//
// NewRing builds a shard for every entry in RingOptions.Addrs, and it does so
// before this function is ever called. OnNewNode only invokes its callback for
// shards created after it is registered, so the callback below never reaches a
// single shard the caller configured up front. Since that is how a ring is
// normally built, the result is that the ring produces no telemetry at all.
func attachRingHooksBuggy(client *redis.Ring) {
	client.OnNewNode(func(rdb *redis.Client) {
		rdb.AddHook(newOtelRedisHook(rdb.Options().Addr))
	})
}

// attachRingHooksFixed keeps the OnNewNode registration, which is still correct
// for shards added later through SetAddrs, and additionally walks the shards
// that already exist so both paths are covered.
func attachRingHooksFixed(client *redis.Ring) {
	client.OnNewNode(func(rdb *redis.Client) {
		rdb.AddHook(newOtelRedisHook(rdb.Options().Addr))
	})
	attachHookToExistingShards(client)
}

// attachHookToExistingShards instruments the shards a ring already holds.
// ForEachShard returns the first error a callback produces; this callback
// cannot fail, so there is nothing to report.
func attachHookToExistingShards(client *redis.Ring) {
	_ = client.ForEachShard(context.Background(), func(_ context.Context, rdb *redis.Client) error {
		rdb.AddHook(newOtelRedisHook(rdb.Options().Addr))
		return nil
	})
}
