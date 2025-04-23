## 1
в одном из релизов мы поняли версию go.opentelemetry.io/otel c v1.29.0 до v1.35.0 и у нас перестал работать jaeger. Появился ошибка "traces export: failed to exit idle mode: invalid target address http://jaeger-collector.observability.svc.cluster.local:14268/api/traces, error info: address http://jaeger-collector.observability.svc.cluster.local:14268/api/traces:443: too many colons in address".  В NewTraceProvider передается адрес струкутра Config с параметром JaegerUrl, заполненым из vault 'http://jaeger-collector.observability.svc.cluster.local:14268/api/traces'. В v1.29.0 это работало, а в v1.35.0 нет. Найди в чем ошибка и предложи решение


## 2
Создай новый тег v0.0.25-RC-0.2, напиши change log на разницу между предыдущей версией и этой

## 3
Обновили версию пакета в приложении, поменяли конфиг на "jaeger-collector.observability.svc.cluster.local:14268", это значение подключается в конфиг в JaegerUrl
В приложении подключается так
```
tp, err := tracing.NewTraceProvider(ctx, &tracing.Config{
		ServiceName: cfg.ServiceName,
		JaegerUrl:   cfg.JaegerUrl,
	})
	if err != nil {
		log.WarnWithCtx(ctx, "Could not create trace provider", logger.Error(err))
	}
```

Теперь выходит такая ошибка
{"level":"info","ts":"2025-04-23T12:38:54Z","logger":"billz_inventory_service_v2","caller":"trace/batch_span_processor.go:326","msg":"traces export: context deadline exceeded: rpc error: code = Unavailable desc = connection error: desc = \"error reading server preface: http2: frame too large\""}
Что нужно исправить и где?