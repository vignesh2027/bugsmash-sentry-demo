package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	sentryotel "github.com/getsentry/sentry-go/otel"
	redis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "bugsmash-redis-ring-demo"

func main() {
	fixed := flag.Bool("fixed", false, "attach hooks the fixed way (walk existing shards) instead of the buggy way")
	addrCSV := flag.String("addrs", "localhost:7001,localhost:7002,localhost:7003", "comma-separated ring shard addresses")
	flag.Parse()

	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		log.Fatal("SENTRY_DSN is not set.\n" +
			"Get one from sentry.io (Settings -> Projects -> your project -> Client Keys),\n" +
			"then: export SENTRY_DSN='https://...ingest.sentry.io/...'")
	}

	mode := "buggy"
	if *fixed {
		mode = "fixed"
	}

	// sentry.Init handles errors and logs. The OTel integration links anything
	// captured here to the active OpenTelemetry trace, so an error and the span
	// it happened inside end up joined in the Sentry UI.
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:         dsn,
		Environment: mode,
		Release:     "redis-ring-shards@" + mode,
		Integrations: func(i []sentry.Integration) []sentry.Integration {
			return append(i, sentryotel.NewOtelIntegration())
		},
	}); err != nil {
		log.Fatalf("sentry.Init: %v", err)
	}
	defer sentry.Flush(5 * time.Second)

	// Spans go to Sentry over OTLP. Every span the redis hook opens travels
	// this path and lands in the Sentry trace view.
	ctx := context.Background()
	counter := newSpanCounter()
	tp, err := newTracerProvider(ctx, dsn, "bugsmash-redis-ring", mode, counter)
	if err != nil {
		log.Fatalf("tracing setup: %v", err)
	}
	otel.SetTracerProvider(tp)
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("tracer shutdown: %v", err)
		}
	}()

	addrs := map[string]string{}
	for i, a := range strings.Split(*addrCSV, ",") {
		addrs[fmt.Sprintf("shard%d", i+1)] = strings.TrimSpace(a)
	}

	ctx, root := otel.Tracer(tracerName).Start(ctx,
		"cart-checkout ("+mode+")",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("demo.mode", mode),
			attribute.Int("demo.ring_shards", len(addrs)),
		),
	)

	// This is the ordering that matters. NewRing constructs every shard listed
	// in Addrs right here, before any hook has been attached to the ring.
	ring := redis.NewRing(&redis.RingOptions{
		Addrs:       addrs,
		DialTimeout: 300 * time.Millisecond,
		ReadTimeout: 300 * time.Millisecond,
		MaxRetries:  -1,
	})
	defer func() { _ = ring.Close() }()

	if *fixed {
		attachRingHooksFixed(ring)
	} else {
		attachRingHooksBuggy(ring)
	}

	// Spread keys across shards so every shard in the ring gets traffic.
	keys := []string{"user:1001", "user:2002", "user:3003", "cart:1001", "cart:2002", "sku:9", "sku:14", "session:abc"}
	for _, k := range keys {
		// The error is deliberately ignored. Whether the command reaches a
		// server is irrelevant: the hook opens its span before handing off to
		// the next hook in the chain, so instrumented shards emit a span either
		// way. That is exactly what makes the difference visible.
		_ = ring.Get(ctx, k).Err()
		_ = ring.Set(ctx, k, "v", time.Minute).Err()
	}

	root.End()

	fmt.Printf("\n  mode:   %s\n", strings.ToUpper(mode))
	fmt.Printf("  shards: %d (%s)\n", len(addrs), *addrCSV)
	fmt.Printf("  ran:    %d commands across the ring\n\n", len(keys)*2)
	fmt.Printf("  spans produced:\n%s\n", counter.report())

	n := counter.redisSpans()
	fmt.Printf("  redis spans observed: %d\n", n)
	if n == 0 {
		fmt.Println("  ^ every command above executed and not one was traced.")
	}
	fmt.Println("\n  Flushing to Sentry...")
}
