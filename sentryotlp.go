package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// sentryOTLP describes where OpenTelemetry spans should be shipped for a given
// Sentry DSN.
type sentryOTLP struct {
	Endpoint  string // host, e.g. o123456.ingest.us.sentry.io
	URLPath   string // /api/4509.../otlp/v1/traces
	PublicKey string
}

// parseDSN turns a Sentry DSN into the OTLP traces endpoint Sentry exposes.
//
// A DSN looks like https://<public_key>@o<org>.ingest.<region>.sentry.io/<project_id>
// and the matching OTLP endpoint is
// https://o<org>.ingest.<region>.sentry.io/api/<project_id>/otlp/v1/traces
// authenticated with an x-sentry-auth header carrying the public key.
func parseDSN(dsn string) (sentryOTLP, error) {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return sentryOTLP{}, fmt.Errorf("parsing DSN: %w", err)
	}
	if u.User == nil || u.User.Username() == "" {
		return sentryOTLP{}, fmt.Errorf("DSN has no public key (expected https://KEY@host/project)")
	}
	projectID := strings.Trim(u.Path, "/")
	if projectID == "" {
		return sentryOTLP{}, fmt.Errorf("DSN has no project id")
	}
	return sentryOTLP{
		Endpoint:  u.Host,
		URLPath:   fmt.Sprintf("/api/%s/otlp/v1/traces", projectID),
		PublicKey: u.User.Username(),
	}, nil
}

// newTracerProvider builds a tracer provider that exports straight to Sentry
// over OTLP. Sampling is forced on so nothing gets dropped while demoing.
func newTracerProvider(ctx context.Context, dsn, serviceName, environment string, counter *spanCounter) (*sdktrace.TracerProvider, error) {
	target, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(target.Endpoint),
		otlptracehttp.WithURLPath(target.URLPath),
		otlptracehttp.WithHeaders(map[string]string{
			"x-sentry-auth": "sentry sentry_key=" + target.PublicKey,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.DeploymentEnvironmentNameKey.String(environment),
	))
	if err != nil {
		return nil, fmt.Errorf("building resource: %w", err)
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSpanProcessor(counter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	), nil
}
