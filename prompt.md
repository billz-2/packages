## 1
в одном из релизов мы поняли версию go.opentelemetry.io/otel c v1.29.0 до v1.35.0 и у нас перестал работать jaeger. Появился ошибка "traces export: failed to exit idle mode: invalid target address http://jaeger-collector.observability.svc.cluster.local:14268/api/traces, error info: address http://jaeger-collector.observability.svc.cluster.local:14268/api/traces:443: too many colons in address".  В NewTraceProvider передается адрес струкутра Config с параметром JaegerUrl, заполненым из vault 'http://jaeger-collector.observability.svc.cluster.local:14268/api/traces'. В v1.29.0 это работало, а в v1.35.0 нет. Найди в чем ошибка и предложи решение


## 2
Создай новый тег v0.0.25-RC-0.2, напиши change log на разницу между предыдущей версией и этой