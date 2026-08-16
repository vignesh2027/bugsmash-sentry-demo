package main

import (
	"context"
	"sort"
	"strings"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// spanCounter is a span processor that tallies spans by name as they end. It
// exists so the demo can prove what it claims locally, without anyone needing
// to open Sentry and count rows by eye.
type spanCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newSpanCounter() *spanCounter {
	return &spanCounter{counts: map[string]int{}}
}

func (c *spanCounter) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (c *spanCounter) OnEnd(s sdktrace.ReadOnlySpan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[s.Name()]++
}

func (c *spanCounter) Shutdown(context.Context) error   { return nil }
func (c *spanCounter) ForceFlush(context.Context) error { return nil }

// redisSpans returns how many spans the redis instrumentation produced.
func (c *spanCounter) redisSpans() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for name, n := range c.counts {
		if strings.HasPrefix(name, "redis.") {
			total += n
		}
	}
	return total
}

// report renders the tally, busiest span first.
func (c *spanCounter) report() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	names := make([]string, 0, len(c.counts))
	for name := range c.counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if c.counts[names[i]] != c.counts[names[j]] {
			return c.counts[names[i]] > c.counts[names[j]]
		}
		return names[i] < names[j]
	})

	var b strings.Builder
	for _, name := range names {
		b.WriteString("    ")
		b.WriteString(name)
		b.WriteString(" x")
		b.WriteString(itoa(c.counts[name]))
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "    (no spans at all)\n"
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
