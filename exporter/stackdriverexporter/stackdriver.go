// Copyright 2019, OpenTelemetry Authors
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

// Package stackdriverexporter contains the wrapper for OpenTelemetry-Stackdriver
// exporter to be used in opentelemetry-collector.
package stackdriverexporter

import (
	"context"
	"fmt"

	"contrib.go.opencensus.io/exporter/stackdriver"
	cloudtrace "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenterror"
	"go.opentelemetry.io/collector/consumer/pdata"
	"go.opentelemetry.io/collector/consumer/pdatautil"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	traceexport "go.opentelemetry.io/otel/sdk/export/trace"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
)

const name = "stackdriver"

// traceExporter is a wrapper struct of OT cloud trace exporter
type traceExporter struct {
	texporter *cloudtrace.Exporter
}

// metricsExporter is a wrapper struct of OC stackdriver exporter
type metricsExporter struct {
	mexporter *stackdriver.Exporter
}

func (*traceExporter) Name() string {
	return name
}

func (*metricsExporter) Name() string {
	return name
}

func (te *traceExporter) Shutdown(context.Context) error {
	te.texporter.Flush()
	return nil
}

func (me *metricsExporter) Shutdown(context.Context) error {
	me.mexporter.Flush()
	me.mexporter.StopMetricsExporter()
	return nil
}

func generateClientOptions(cfg *Config) ([]option.ClientOption, error) {
	var copts []option.ClientOption
	if cfg.UseInsecure {
		conn, err := grpc.Dial(cfg.Endpoint, grpc.WithInsecure())
		if err != nil {
			return nil, fmt.Errorf("cannot configure grpc conn: %w", err)
		}
		copts = append(copts, option.WithGRPCConn(conn))
	} else {
		copts = append(copts, option.WithEndpoint(cfg.Endpoint))
	}
	return copts, nil
}

func newStackdriverTraceExporter(cfg *Config) (component.TraceExporter, error) {
	topts := []cloudtrace.Option{
		cloudtrace.WithProjectID(cfg.ProjectID),
	}
	if cfg.Endpoint != "" {
		copts, err := generateClientOptions(cfg)
		if err != nil {
			return nil, err
		}
		topts = append(topts, cloudtrace.WithTraceClientOptions(copts))
	}
	if cfg.NumOfWorkers > 0 {
		topts = append(topts, cloudtrace.WithMaxNumberOfWorkers(cfg.NumOfWorkers))
	}
	exp, err := cloudtrace.NewExporter(topts...)
	if err != nil {
		return nil, fmt.Errorf("error creating Stackdriver Trace exporter: %w", err)
	}
	tExp := &traceExporter{texporter: exp}

	return exporterhelper.NewTraceExporter(
		cfg,
		tExp.pushTraces,
		exporterhelper.WithShutdown(tExp.Shutdown))
}

func newStackdriverMetricsExporter(cfg *Config) (component.MetricsExporter, error) {
	// TODO:  For each ProjectID, create a different exporter
	// or at least a unique Stackdriver client per ProjectID.
	options := stackdriver.Options{
		// If the project ID is an empty string, it will be set by default based on
		// the project this is running on in GCP.
		ProjectID: cfg.ProjectID,

		MetricPrefix: cfg.Prefix,

		// Set DefaultMonitoringLabels to an empty map to avoid getting the "opencensus_task" label
		DefaultMonitoringLabels: &stackdriver.Labels{},
	}
	if cfg.Endpoint != "" {
		copts, err := generateClientOptions(cfg)
		if err != nil {
			return nil, err
		}
		options.TraceClientOptions = copts
		options.MonitoringClientOptions = copts
	}
	if cfg.NumOfWorkers > 0 {
		options.NumberOfWorkers = cfg.NumOfWorkers
	}
	if cfg.SkipCreateMetricDescriptor {
		options.SkipCMD = true
	}
	if len(cfg.ResourceMappings) > 0 {
		rm := resourceMapper{
			mappings: cfg.ResourceMappings,
		}
		options.MapResource = rm.mapResource
	}
	sde, serr := stackdriver.NewExporter(options)
	if serr != nil {
		return nil, fmt.Errorf("cannot configure Stackdriver metric exporter: %w", serr)
	}
	mExp := &metricsExporter{mexporter: sde}

	return exporterhelper.NewMetricsExporter(
		cfg,
		mExp.pushMetrics,
		exporterhelper.WithShutdown(mExp.Shutdown))
}

// pushMetrics calls StackdriverExporter.PushMetricsProto on each element of the given metrics
func (me *metricsExporter) pushMetrics(ctx context.Context, m pdata.Metrics) (int, error) {
	var errors []error
	var totalDropped int

	mds := pdatautil.MetricsToMetricsData(m)
	for _, md := range mds {
		_, points := pdatautil.TimeseriesAndPointCount(md)
		dropped, err := me.mexporter.PushMetricsProto(ctx, md.Node, md.Resource, md.Metrics)
		recordPointCount(ctx, points-dropped, dropped, err)
		totalDropped += dropped
		if err != nil {
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		return totalDropped, componenterror.CombineErrors(errors)
	}

	return totalDropped, nil
}

// pushTraces calls texporter.ExportSpan for each span in the given traces
func (te *traceExporter) pushTraces(ctx context.Context, td pdata.Traces) (int, error) {
	var errs []error
	resourceSpans := td.ResourceSpans()
	numSpans := td.SpanCount()
	goodSpans := 0
	spans := make([]*traceexport.SpanData, 0, numSpans)

	for i := 0; i < resourceSpans.Len(); i++ {
		sd, err := pdataResourceSpansToOTSpanData(resourceSpans.At(i))
		if err == nil {
			spans = append(spans, sd...)
		} else {
			errs = append(errs, err)
		}
	}

	for _, span := range spans {
		te.texporter.ExportSpan(ctx, span)
		goodSpans++
	}

	return numSpans - goodSpans, componenterror.CombineErrors(errs)
}
