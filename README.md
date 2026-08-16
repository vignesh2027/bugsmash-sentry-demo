# Redis ring telemetry blackout — a Sentry reproduction

A minimal Go program that makes a real OpenTelemetry instrumentation bug visible
in Sentry.

The bug is in [opentelemetry-go-compile-instrumentation](https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation):
the hook that instruments `redis.Ring` clients never reached any shard the
caller configured up front, so ring clients produced **no telemetry at all**.

Fix: [open-telemetry/opentelemetry-go-compile-instrumentation#1098](https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation/pull/1098)

## The bug in one paragraph

`NewRing` builds a shard for every entry in `RingOptions.Addrs`, and it finishes
doing that before the instrumentation hook ever runs. The hook registered its
callback through `OnNewNode`, which only fires for shards created *after*
registration. So every shard from `Addrs` — which is how a ring is normally
built — was skipped. No error, no warning, just an empty trace.

```go
// before: looks correct, instruments nothing
func attachRingHooksBuggy(client *redis.Ring) {
	client.OnNewNode(func(rdb *redis.Client) {
		rdb.AddHook(newOtelRedisHook(rdb.Options().Addr))
	})
}

// after: keep OnNewNode for shards added later, and walk the ones already there
func attachRingHooksFixed(client *redis.Ring) {
	client.OnNewNode(func(rdb *redis.Client) {
		rdb.AddHook(newOtelRedisHook(rdb.Options().Addr))
	})
	attachHookToExistingShards(client)
}
```

## Run it

You need a Sentry DSN. No Redis server required — see "Why no Redis" below.

```sh
export SENTRY_DSN='https://<key>@o<org>.ingest.<region>.sentry.io/<project>'

go run .          # buggy hook attachment
go run . -fixed   # fixed hook attachment
```

Both runs execute exactly the same 16 Redis commands across a 3-shard ring.

## What you get

```
  mode:   BUGGY                        mode:   FIXED
  ran:    16 commands                  ran:    16 commands

  spans produced:                      spans produced:
    cart-checkout (buggy) x1             redis.ping x9
                                         redis.get  x6
                                         redis.set  x5
                                         cart-checkout (fixed) x1

  redis spans observed: 0              redis spans observed: 20
```

In Sentry, the buggy run arrives as a `cart-checkout` transaction with nothing
underneath it. The fixed run arrives with the same transaction and every command
hanging off it, tagged with the shard that served it.

The `redis.ping` spans are the ring's own shard health checks. Those were
invisible too, which means a ring silently losing a shard would not have shown
up either.

## Why no Redis server

Every shard points at a port nothing is listening on. That is deliberate.

The instrumentation hook opens its span *before* handing the command to the next
hook in the chain, so a command that never reaches a server still produces a
span. The failure path runs through the same instrumentation as the success
path. That makes the demo runnable anywhere with no container, no fixture, and
no CI dependency — and it is the same trick the upstream tests use.

If you would rather see successful commands, start one on each port:

```sh
docker run -d -p 7001:6379 redis:alpine
docker run -d -p 7002:6379 redis:alpine
docker run -d -p 7003:6379 redis:alpine
```

## Layout

| File | What it holds |
| --- | --- |
| `main.go` | Wires Sentry, builds the ring, runs the commands |
| `redishook.go` | The redis hook, plus the buggy and fixed attachment functions |
| `sentryotlp.go` | Turns a Sentry DSN into an OTLP traces endpoint |
| `counter.go` | Counts spans locally so the result is provable without opening Sentry |

## How spans reach Sentry

Sentry ingests OpenTelemetry directly over OTLP. `sentryotlp.go` derives the
endpoint from the DSN:

```
DSN   https://<key>@o<org>.ingest.<region>.sentry.io/<project>
OTLP  https://o<org>.ingest.<region>.sentry.io/api/<project>/otlp/v1/traces
auth  x-sentry-auth: sentry sentry_key=<key>
```

`sentry.Init` still runs alongside it with `sentryotel.NewOtelIntegration()`, so
any error captured through the Sentry SDK is linked to the active OTel trace.
