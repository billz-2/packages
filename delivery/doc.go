// Package delivery is the root of a module for the reliable Kafka delivery of a service: package outbox
// sends the records written in the transaction of a business change, package inbox records the messages a
// consumer handled. The packages import only the standard library, sarama, cloudevents (outbox/ce) and the
// Prometheus client (the *prom packages), so that a service gets no other dependency from them. See
// README.md for how a service plugs them in.
package delivery
