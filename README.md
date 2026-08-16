<div align="center">

# Redis Ring Telemetry Blackout

**A real OpenTelemetry bug, reproduced in Sentry.**

Ring clients produced no telemetry at all. Not partial data, not wrong data: nothing.

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Sentry](https://img.shields.io/badge/Sentry-OTLP-362D59?logo=sentry&logoColor=white)](https://sentry.io)
[![Fix](https://img.shields.io/badge/upstream%20PR-%231098-brightgreen)](https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation/pull/1098)

</div>

<img alt="Redis ring traced two ways" src="https://github.com/user-attachments/assets/42c8eb81-7aa4-47c5-8fa4-3a9dfad287b3" />

---

## The result

Same program, same 3 shard ring, same 16 Redis commands, same 4.5 seconds of runtime.
The only thing that changes is how the instrumentation hook gets attached.

<table>
<tr>
<th width="50%">Before</th>
<th width="50%">After</th>
</tr>
<tr>
<td>

```
mode:   BUGGY
ran:    16 commands

spans produced:
  cart-checkout (buggy) x1

redis spans observed: 0
```

</td>
<td>

```
mode:   FIXED
ran:    16 commands

spans produced:
  redis.ping x9
  redis.get  x6
  redis.set  x5
  cart-checkout (fixed) x1

redis spans observed: 20
```

</td>
</tr>
<tr>
<td align="center"><b>An empty waterfall.</b><br>Every command ran. Nothing watched.</td>
<td align="center"><b>Every command traced,</b><br>tagged with the shard that served it.</td>
</tr>
</table>

### Seen in Sentry

**Before.** One transaction, 4.49 seconds, nothing underneath it. Sixteen Redis commands ran inside that window and the trace shows none of them.

<img alt="cart-checkout (buggy): a single span, no children" src="https://github.com/user-attachments/assets/bfe09b9d-cd49-4045-acac-e852f0cd360b" />

**After.** Same code, same commands, same 4.5 seconds. Every command traced, each one tagged with the shard that served it.

<img alt="cart-checkout (fixed): the full waterfall" src="https://github.com/user-attachments/assets/62f09ec6-e0d7-42f5-9d73-8d233f42b0a5" />

---

## The bug

`NewRing` builds a shard for every entry in `RingOptions.Addrs`, and it finishes doing
that before the instrumentation hook ever runs.

The hook registered its callback through `OnNewNode`, which only fires for shards
created **after** registration. So every shard from `Addrs`, which is how a ring is
normally built, got skipped.

No error. No warning. Just an empty trace, and absence reads as health.

```go
// before: reads as correct, instruments nothing
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

Upstream fix: [open-telemetry/opentelemetry-go-compile-instrumentation#1098](https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation/pull/1098)

---

## Run it

You need a Sentry DSN. You do **not** need a Redis server.

```sh
export SENTRY_DSN='https://<key>@o<org>.ingest.<region>.sentry.io/<project>'

go run .          # buggy hook attachment
go run . -fixed   # fixed hook attachment
```

Then open **Explore : Traces** in Sentry and compare the two `cart-checkout`
transactions.

---

## Errors, not just spans

The failed commands are captured on a scope tagged with the operation, the key and
which hook mode was running, so the Issue says what happened rather than only that
something did. The capture happens inside the active span, so the error and the span
it came from stay linked.

<img alt="Failed commands grouped as Sentry Issues" src="https://github.com/user-attachments/assets/1efff1b0-ccb9-46bf-b3f5-df787855e7e9" />

<img alt="Root cause analysis" src="https://github.com/user-attachments/assets/bca6526e-d861-4960-8926-b746f82b84ab" />

---

## Why no Redis server is needed

Every shard points at a port nothing is listening on, on purpose.

The instrumentation hook opens its span **before** handing the command to the next hook
in the chain, so a command that never reaches a server still produces a span. The failure
path runs through the same instrumentation as the success path.

That makes this runnable anywhere: no container, no fixture, no CI dependency. It is the
same trick the upstream tests use.

Prefer successful commands? Start three:

```sh
docker run -d -p 7001:6379 redis:alpine
docker run -d -p 7002:6379 redis:alpine
docker run -d -p 7003:6379 redis:alpine
```

---

## How spans reach Sentry

Sentry ingests OpenTelemetry directly over OTLP, so the spans you see are the real
instrumentation behaving exactly as it would in production, not a reimplementation.

```
DSN    https://<key>@o<org>.ingest.<region>.sentry.io/<project>
OTLP   https://o<org>.ingest.<region>.sentry.io/api/<project>/integration/otlp/v1/traces
auth   x-sentry-auth: sentry sentry_key=<key>
```

Two things worth knowing before you try this yourself:

1. That `/integration/` segment is easy to miss. Leave it out and the endpoint answers
   `404 Not Found` with an empty body, which tells you nothing about which half of the
   URL was wrong.
2. OTLP ingestion is in open beta and span **events** are dropped during ingest. This
   demo keeps its diagnostic detail in span **attributes** instead, which turned out to
   be the better design regardless.

`sentry.Init` runs alongside the exporter with `sentryotel.NewOtelIntegration()`, so any
error captured through the Sentry SDK is linked to the active OpenTelemetry trace.

> Note: `sentry-go` v0.48 removed `NewSentrySpanProcessor`. The tracer provider pattern
> most tutorials still show will not compile. OTLP export is the current path.

---

## Layout

| File | What it holds |
| :--- | :--- |
| `main.go` | Wires Sentry, builds the ring, runs the commands |
| `redishook.go` | The Redis hook, plus the buggy and fixed attachment functions |
| `sentryotlp.go` | Turns a Sentry DSN into an OTLP traces endpoint |
| `counter.go` | Counts spans locally, so the result is provable without opening Sentry |

The span counter exists on purpose. The evidence travels with the code, so nobody has to
take a screenshot's word for it.
