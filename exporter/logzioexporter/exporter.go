// Copyright -c Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logzioexporter

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/go-hclog"
	"github.com/jaegertracing/jaeger/model"
	"github.com/logzio/jaeger-logzio/store"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer/pdata"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	"go.opentelemetry.io/collector/translator/trace/jaeger"
)

const (
	loggerName = "logzio-exporter"
)

// exporter exporters OpenTelemetry Collector data to New Relic.
type logzioExporter struct {
	accountToken string
	writer       *store.LogzioSpanWriter
	logger       hclog.Logger
}

var WriteSpanFunc func(span *model.Span) error
var InternalTracesToJaegerTraces = jaeger.InternalTracesToJaegerProto

func newLogzioExporter(config *Config, params component.ExporterCreateParams) (*logzioExporter, error) {
	logger := hclog.New(&hclog.LoggerOptions{
		Level:      hclog.Info,
		Name:       loggerName,
		JSONFormat: true,
	})

	if config == nil {
		return nil, errors.New("exporter config can't be null")
	}
	writerConfig := store.LogzioConfig{
		Region:            config.Region,
		AccountToken:      config.Token,
		CustomListenerURL: config.CustomListenerAddress,
	}

	spanWriter, err := store.NewLogzioSpanWriter(writerConfig, logger)
	if err != nil {
		return nil, err
	}

	return &logzioExporter{
		writer:       spanWriter,
		accountToken: config.Token,
		logger:       logger,
	}, nil
}

func newLogzioTraceExporter(config *Config, params component.ExporterCreateParams) (component.TraceExporter, error) {
	exporter, err := newLogzioExporter(config, params)
	if err != nil {
		return nil, err
	}
	WriteSpanFunc = exporter.writer.WriteSpan
	if err := config.validate(); err != nil {
		return nil, err
	}

	return exporterhelper.NewTraceExporter(
		config,
		exporter.pushTraceData,
		exporterhelper.WithShutdown(exporter.Shutdown))
}

func (exporter *logzioExporter) pushTraceData(ctx context.Context, traces pdata.Traces) (droppedSpansCount int, err error) {
	droppedSpans := 0
	batches, err := InternalTracesToJaegerTraces(traces)
	if err != nil {
		return traces.SpanCount(), err
	}
	for _, batch := range batches {
		for _, span := range batch.Spans {
			span.Process = batch.Process
			if err := WriteSpanFunc(span); err != nil {
				exporter.logger.Debug(fmt.Sprintf("dropped bad span: %s", span.String()))
				droppedSpans++
			}
		}
	}
	return droppedSpans, nil
}

func (exporter *logzioExporter) Shutdown(ctx context.Context) error {
	exporter.logger.Info("Closing logzio exporter..")
	exporter.writer.Close()
	return nil
}
