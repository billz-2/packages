# delivery

Reliable Kafka delivery for a service on PostgreSQL: a transactional **outbox** for what it sends, an
**inbox** for what it consumes.

```
go get github.com/billz-2/packages/delivery@v0.1.0
```

| package | what it does |
|---|---|
| `outbox` | writes Kafka records into a table in the caller's transaction; a relay in every pod sends them and deletes each one once Kafka acknowledged it |
| `outbox/ce` | a cloudevents publisher on top of the outbox (`Push`, `PushTx`), records encoded as the cloudevents `kafka_sarama` sender encodes them |
| `outbox/outboxprom` | Prometheus metrics of the outbox |
| `inbox` | records every consumed message in a table: duplicates are skipped, failures are retried in process and kept as dead letters, an offset is marked only once its row is written; same-transaction handlers, deferred (long) handlers |
| `inbox/inboxprom` | Prometheus metrics of the inbox |

It is its own Go module, apart from the root `github.com/billz-2/packages`, so that a service can take it
without upgrading the root module (logger, tracing). The packages import only the standard library, sarama,
cloudevents (`outbox/ce` only) and the Prometheus client (the `*prom` packages); `imports_test.go` keeps it so.
They take `database/sql` types and a slog-shaped `Logger` (`Info`, `Warn`, `Error` with key-value pairs).

The first service on it is `billz_inventory_service_v2`; its `docs/outbox.md` and `docs/inbox.md` are the
runbook of a service in production and show every option below in use.

## Guarantees and limits

**Outbox**

- A record written with `PushTx` / `Publish(ctx, tx, …)` is sent if and only if `tx` commits. `Push` (no
  transaction) writes at once on the outbox's pool; a nil error means "stored, will be sent", also while
  Kafka is down.
- At least once. A round that does not commit sends its acknowledged rows again; consumers deduplicate on
  the `ce_id` header (the inbox does).
- Per topic and key, records reach Kafka in the order of their rows (id order among the committed rows: a
  later insert that commits first can go first): one at a time, the next only after Kafka acknowledged the
  one before; a failing record holds back the rest of its topic, with a back-off of
  1 s doubling to 30 s. Other topics go on.
- A record that can never be sent (too large, an invalid topic, `INVALID_RECORD`, a topic unknown for
  2 minutes) moves to `outbox_failed`, and the rest of its key goes on without it: its key's order is broken
  at that record.
- One round at a time across all pods (an advisory lock). A pod lost in a round holds the outbox for at most
  `IdleTimeout` (60 s).
- Order can still break when the round's PostgreSQL session ends while its send is still at Kafka and the
  pod lives on (a partition from PostgreSQL over 60 s, a failover): another pod sends the same rows while
  sarama may still deliver them.
- `acks=all` is only as strong as the topic: see "Kafka durability" below.
- PostgreSQL 12 or later.

**Inbox**

- Key of a row: `(consumer, event_id, digest)` = the consumer name, the `ce_id` header (or
  `topic/partition/offset` without one) and the first 16 bytes of the value's sha256. A redelivery is
  skipped while its done row is kept (`Retention`, 72 h by default). Two messages with the same `ce_id`
  and value but other headers count as one.
- `After`: the handler runs with its own writes, then the done row (and its row writes, `OnRow`) are
  written in one transaction. A crash between the two, or a second delivery while it runs, runs the handler
  twice.
- `InTx`: the claim of the row, the handler's writes, its outbox records and its answer are one
  transaction: all or nothing. A second delivery waits for the first one's commit and is skipped.
- A partition runs one message at a time; a failing message runs again in process, never through a
  rebalance.
- `Defer`: one message of a key at a time across the pods, under a lease of 2 minutes kept by heartbeats; a
  lost pod's message runs again after the lease, so a deferred handler must be safe to cut and run again.
- Dead letters stay in the table (status 2) with their payload, headers, error and attempts until
  `DeadRetention`, or for ever when it is 0.

## Plugging a service in

### 1. Migrations

The packages render their DDL from embedded templates; the service ships it as its own numbered migration
files, applied by its migrate step before the pods start.

```go
up, down, err := outbox.Migration(outbox.Options{})  // tables outbox and outbox_failed
inUp, inDown, err := inbox.Migration(inbox.Options{}) // table processed_events
```

Write the four strings once into `migrations/postgres/NNNNNN_outbox.{up,down}.sql` and
`NNNNNN_processed_events.{up,down}.sql`, with a small command such as inventory's `scripts/genmigrations`,
and keep a test that compares the files with the rendered strings. Pass the same table names to
`Migration` and to `New`. An applied migration file is never changed: when a new version of this module
changes a template, the service adds a new migration for the difference.

At start, `Outbox.Check` and `Inbox.Check` (with a timeout of about 10 s) fail when a table is missing; a pod
should exit then rather than fail its first business transaction.

### 2. The outbox, the publisher and the relay

```go
ob, err := outbox.New(db, outbox.Options{Service: "billz_order_service", Logger: log, Metrics: obMetrics})
pub := ce.NewPublisher(ob, ce.Options{PartitionKeyExtension: "partitionkey",
	MaxMessageBytes: producerConfig.Producer.MaxMessageBytes})
pub.AddTopic("v2.order_service.sale.deleted") // Push refuses a topic that was not added
```

- `Options.Service` is required; the relay's lock key and session name derive from it. Keep `LockKey` and
  `ApplicationName` fixed once in production: every pod of a table must share the lock, the pods of the
  previous version during a deploy too.
- The relay's sarama clients need `outbox.ProducerConfig(cfg)`: `acks=all`, idempotent, one request in
  flight per broker. `outbox.NewProducer(client)` refuses a client without it.
- Give the relay a pool of its own (2 connections, `application_name` = the session name), so a busy main
  pool does not stall it. `outbox.Router` sends each topic through the producer of its choice (for example
  a separate client for heavy topics).
- A statement cache on the relay's connections (pgx, prepared statements through pgbouncer) is fine: each
  round forces a custom plan for its read.

Start-up and shutdown:

```go
relay, err := ob.Relay(relayDB, producer, outbox.RelayOptions{})
relay.Start()
go ob.ReportDurability(ctx, admin, pub.Topics(), 4) // once the topics are added; never fails

// at shutdown, after the consumers and the background tasks, so that it sends what they wrote:
err = relay.Close() // up to RelayOptions.CloseTimeout (5 s), then the producer and the clients
```

`Close` stops the round in progress before its next wave and commits what Kafka acknowledged. A send still
stuck after `CloseTimeout` keeps its lock until it ends or the process exits; `Close` returns an error and
the shutdown goes on.

### 3. Consumers with the inbox

```go
in, err := inbox.New(db, inbox.Options{Service: "billz_order_service", DeadRetention: 14 * 24 * time.Hour,
	Logger: log, Metrics: inMetrics})
in.LimitTx(maxOpenConns / 2) // InTx transactions open at once; keep the rest of the pool for other work

consumer := inbox.NewConsumer(group, inbox.ConsumerOptions{Logger: log})
consumer.Handle("v2.order_service.sale.delete", in.InTx("v2.order_service.sale.delete", begin, deleteSale,
	inbox.DefaultRetry))
consumer.Handle("v2.order_service.report.build", in.Defer("v2.order_service.report.build", byCompany,
	buildReport, inbox.RetryPolicy{}))
<-consumer.Run(ctx) // closed once the first session is set up

worker, err := in.Worker(inbox.WorkerOptions{HeartbeatDB: heartbeatDB})
worker.Start(ctx) // runs deferred and replayed messages, and the retention
```

The consumer name is the first argument of `After`, `InTx` and `Defer`; inventory uses the topic. It is part
of the row key: two deployments that share a database and consume one topic under one name deduplicate each
other.

- **`InTx`.** `begin` (`inbox.TxStart`) begins the transaction on the service's database; the handler gets
  it as `inbox.Tx(ctx)`. The inbox swaps no pool: a statement on any other connection runs, and commits,
  outside the message. The service decides how its repositories reach that transaction (inventory joins its
  pool to it). Write the handler's events with `PushTx(ctx, tx, …)` in it, and its answer with
  `inbox.OnRow`. Each open transaction holds a connection for the whole handler, HTTP calls included.
- **How a handler fails.** It returns the error of a transient cause (`inbox.Transient`: a lost connection, a
  timeout, a deadlock or serialization failure, too many connections, a server shutting down): the attempt
  is rolled back and runs again by its `RetryPolicy`. Or it returns nil with its failure answer (`OnRow`):
  the message is done. Or it returns `inbox.Abort(err)` when none of its work may commit: its work is
  rolled back and the message is done with its failure writes (`OnFailedRow`, else `OnRow`). A handler whose
  signature cannot return the error leaves it with `inbox.SetCause`, and its caller reads it with the func
  of `inbox.WithCause` and returns it. `inbox.Permanent(err)` makes a dead letter at once.
- **Retry.** `inbox.DefaultRetry` is 4 attempts with 1, 5 and 15 s between them: about 21 s, inside sarama's
  60 s rebalance timeout. Retry only a handler whose second run repeats nothing: an `InTx` handler (its
  failed attempt committed nothing) or one idempotent write. A retried handler must not call a service that
  writes.
- **Dead letters.** After the last attempt, a message whose cause is still transient, or whose handler
  panicked, is a dead row (status 2) with the failure writes of its last attempt. Every other failure is a
  done row with its failure answer. A dead message delivered again is skipped.
- **`Replay`.** `in.Replay(ctx, consumer, eventID)` queues a dead letter again; the worker of a pod that
  registered that consumer runs it with its handler and retry policy. Replay only an idempotent handler, or
  an `InTx` one (a dead letter of it committed nothing).
- **`Defer`.** For a handler that runs longer than a partition may wait: the message is queued (status 1)
  and its offset marked at once; a `Worker` runs it later, one message of a key at a time across the pods.
  Its run can be cut at any moment (the lease, the shutdown) and then runs again on another pod, so it stops
  when its context ends, or writes its results only through `OnRow` or a transaction bound to that context.
  The heartbeats need a pool of their own (`WorkerOptions.HeartbeatDB`, 2 connections).

### 4. Work after the commit

A handler often starts work that must not run inside its transaction: a sync to another store, a slow
answer, a cache refresh. Inventory learned three bugs here; the rules below avoid them.

```go
// in the handler
items := slices.Clone(req.Items) // 1. take what the task needs now
start := func() {
	tasks.Go(func() { // the service's executor, one that Drain waits for
		ctx := inbox.TaskContext(processCtx, ctx) // 2. the message's values, the process's end
		syncItems(ctx, items)
	})
}
if !inbox.AfterCommit(ctx, start) { // 3. after the commit, never after a rollback
	start() // not in an InTx message: start now
}
```

- **Take the data before scheduling.** The task runs after the handler returned; a handler that changes or
  clears what it passed (a slice, a struct behind a pointer) sends the task the changed data. Copy it, or
  encode the event, before `AfterCommit`.
- **Use `inbox.TaskContext(process, msg)`, not the message's context.** The worker cancels a message's
  context as soon as the handler returns, so a task on it fails with `context canceled`. TaskContext keeps
  the message's values (trace, company, logger fields) without its transaction, row writes and cause slot
  (`inbox.Detach`), and takes `Done`, `Err` and the deadline from `process`. `context.WithoutCancel` is not
  enough: nothing would end the task at shutdown.
- **Tasks are fire-and-forget.** A task lost with its pod or cut at shutdown is not run again. Whatever must
  not be lost goes through `PushTx` in the message's transaction (an event a consumer acts on), not in a
  task.
- **Failure answers go through the inbox.** An answer may wait for a task (a delayed success answer, after
  the syncs get a head start), but a failure answer, and the "not saved" answer that replaces a success
  whose work rolled back, are written with `OnRow` / `OnFailedRow` so they commit with the row.
- **Drain, then cancel, at shutdown.** After the consumers stop, wait for the running tasks for a budget
  (inventory: 8 s), then cancel `process` so the tasks left stop before the relay closes and the pools
  close. Set `process` to the root context the shutdown cancels.

Inventory's `helper.Background` and `TaskExecutor.Submit` are this pattern on its own executor.

### 5. Shutdown order

| # | step | inventory's budget |
|---|---|---|
| 0 | HTTP drain delay: keep serving while the load balancer takes the pod out | 5 s |
| 1 | HTTP server | 10 s |
| 2 | `Consumer.Stop` and `Worker.Stop` together: no new message, the running ones finish and are recorded and marked, the session commits, the pod leaves the group | 20 s |
| 3 | drain the post-commit tasks, the root context still alive | 8 s |
| 4 | cancel the root context: ends the tasks left | |
| 5 | `Relay.Close`, then the producers and clients | 6 s |

The sum plus overruns must fit `terminationGracePeriodSeconds`. A message still running at the end of
step 2 is cut by the cancel and runs again after the restart.

### 6. Metrics and alerts

`outboxprom.New(reg, namespace)` and `inboxprom.New(reg, namespace)` register the series with the
service's namespace (inventory: `billz_inventory_service_v2`); pass them as `Options.Metrics`.

| metric | |
|---|---|
| `<ns>_outbox_sent_total{topic}` | records Kafka acknowledged |
| `<ns>_outbox_send_failures_total{topic}` | failed sends; each is tried again, or set aside |
| `<ns>_outbox_set_aside_total{topic}` | records moved to `outbox_failed` |
| `<ns>_outbox_oldest_row_age_seconds` | age of the oldest row, 0 when empty, read by every relay every 15 s |
| `<ns>_outbox_kafka_weak_topics` | published topics on which `acks=all` can still lose a record, or that could not be described; -1 when the check did not run |
| `<ns>_inbox_messages_total{consumer,result}` | `done`, `duplicate`, `retry`, `dead`, `skipped`, `queued` |
| `<ns>_inbox_tx_duration_seconds{consumer}` | an `InTx` attempt's transaction |
| `<ns>_inbox_tx_slot_wait_seconds{consumer}` | the wait for an `InTx` slot (`LimitTx`) |
| `<ns>_inbox_dead_rows` | dead rows kept, read every 60 s |
| `<ns>_inbox_queued_oldest_age_seconds{consumer}` | the oldest queued message of a deferred consumer |

```
max(<ns>_outbox_oldest_row_age_seconds) > 60     for 2m   warning
max(<ns>_outbox_oldest_row_age_seconds) > 300    for 1m   page
absent(<ns>_outbox_oldest_row_age_seconds)       for 5m   page      # no relay runs
sum by (topic) (increase(<ns>_outbox_set_aside_total[10m])) > 0     page
sum by (topic) (increase(<ns>_outbox_send_failures_total[5m])) > 0  warning
<ns>_outbox_kafka_weak_topics != 0               for 10m  warning   # staging and prod
increase(<ns>_inbox_messages_total{result="dead"}[15m]) > 0         warning
increase(<ns>_inbox_messages_total{result="skipped"}[15m]) > 0      page
<ns>_inbox_queued_oldest_age_seconds > 900                          warning
```

### 7. Kafka durability

The outbox deletes a row once Kafka acknowledged it, so the topic must keep what it acknowledged:

- **acks=all** and **idempotence**: `outbox.ProducerConfig`. The idempotent producer asks for a producer id at
  start (`InitProducerId`); on brokers older than 2.8 that needs the **`IdempotentWrite` cluster ACL** for the
  service principal, or the relay does not start.
- **3 replicas, `min.insync.replicas` 2, `unclean.leader.election.enable=false`** on every published topic.
  With fewer, the in-sync set can shrink to the leader, and a lost leader takes records the outbox already
  deleted: the guarantee is then that of `acks=1`.
- `ReportDurability` checks the published topics after the start (read-only; the admin client needs the
  `DescribeConfigs` ACL), logs an Error per weak topic and sets `outbox_kafka_weak_topics`. It never stops
  the pod. `CheckDurability` is the same check for a script.
- The service principal needs write ACLs on every topic it publishes: a missing one holds that topic (the
  others go on) until it is fixed.
- With 2 of 3 replicas down the brokers refuse writes (`NOT_ENOUGH_REPLICAS`): the rows wait and go out in
  order once the replicas are back; nothing is lost or set aside.

## Example: an order_service-style wiring

```go
func run(ctx context.Context, cfg Config, log outbox.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	db, _ := sql.Open("postgres", cfg.DSN)               // the main pool
	relayDB := openPool(cfg.DSN, "order-outbox-relay", 2) // pools of their own
	heartbeatDB := openPool(cfg.DSN, "order-inbox-heartbeat", 2)

	obMetrics, _ := outboxprom.New(prometheus.DefaultRegisterer, "billz_order_service")
	inMetrics, _ := inboxprom.New(prometheus.DefaultRegisterer, "billz_order_service")
	ob, _ := outbox.New(db, outbox.Options{Service: "billz_order_service", Logger: log, Metrics: obMetrics})
	in, _ := inbox.New(db, inbox.Options{Service: "billz_order_service", DeadRetention: 14 * 24 * time.Hour,
		Logger: log, Metrics: inMetrics})
	if err := errors.Join(check(ctx, ob.Check), check(ctx, in.Check)); err != nil {
		return err // the migrations are not applied
	}

	pc := sarama.NewConfig()
	outbox.ProducerConfig(pc)
	client, _ := sarama.NewClient(cfg.Brokers, pc)
	producer, _ := outbox.NewProducer(client)
	pub := ce.NewPublisher(ob, ce.Options{PartitionKeyExtension: "partitionkey",
		MaxMessageBytes: pc.Producer.MaxMessageBytes})
	pub.AddTopic("v2.order_service.sale.deleted")
	pub.AddTopic("v2.order_service.payment.created")

	relay, _ := ob.Relay(relayDB, producer, outbox.RelayOptions{})
	relay.Start()

	// the service's helper.Background: after the commit, ended by the root context
	afterCommit := func(msg context.Context, task func(ctx context.Context)) {
		taskCtx := inbox.TaskContext(ctx, msg)
		start := func() { tasks.Go(func() { task(taskCtx) }) }
		if !inbox.AfterCommit(msg, start) {
			start()
		}
	}

	in.LimitTx(cfg.MaxOpenConns / 2)
	begin := func(ctx context.Context) (*sql.Tx, func(), error) {
		tx, err := db.BeginTx(ctx, nil)
		return tx, nil, err
	}
	deleteSale := func(ctx context.Context, m *inbox.Message) error {
		tx, _ := inbox.Tx(ctx)
		req, err := decodeSaleDelete(m.Value)
		if err != nil {
			inbox.OnRow(ctx, answer(pub, m, "v2.order_service.sale.delete.failed", err))
			return nil // done with its failure answer
		}
		if err = sales.Delete(ctx, tx, req.SaleID); inbox.Transient(err) {
			return err // rolled back, runs again
		} else if err != nil {
			inbox.OnRow(ctx, answer(pub, m, "v2.order_service.sale.delete.failed", err))
			return inbox.Abort(err) // nothing of it commits
		}
		e := saleDeletedEvent(req) // built now, from what the handler holds
		if err = pub.PushTx(ctx, tx, "v2.order_service.sale.deleted", e, req.CompanyID); err != nil {
			return err
		}
		afterCommit(ctx, func(ctx context.Context) { refreshSearch(ctx, req.SaleID) })
		return nil
	}

	group, _ := sarama.NewConsumerGroup(cfg.Brokers, cfg.GroupID, sarama.NewConfig())
	consumer := inbox.NewConsumer(group, inbox.ConsumerOptions{Logger: log})
	consumer.Handle("v2.order_service.sale.delete",
		in.InTx("v2.order_service.sale.delete", begin, deleteSale, inbox.DefaultRetry))
	worker, _ := in.Worker(inbox.WorkerOptions{HeartbeatDB: heartbeatDB})
	worker.Start(ctx)
	<-consumer.Run(ctx)
	go ob.ReportDurability(ctx, admin(cfg), pub.Topics(), 4)

	<-stopSignal()
	stopCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	_ = errors.Join(consumer.Stop(stopCtx), worker.Stop(stopCtx))
	stop()
	tasks.Drain(8 * time.Second)
	cancel() // ends the tasks the drain left
	return errors.Join(relay.Close(), producer.Close(), client.Close())
}
```

The helpers are the service's own: `openPool` opens a pool with a size and an `application_name`, `check`
runs a check with a 10 s timeout, `answer` builds the response event and returns the func `OnRow` takes (it
writes the event with `pub.PushTx(ctx, tx, …)`), and `tasks` is an executor whose `Drain` waits for its
running tasks.

## Versioning

- A nested module: tags are `delivery/vX.Y.Z` on the `billz-2/packages` repository (`git tag -a
  delivery/v0.1.0 -m "delivery/v0.1.0: outbox and inbox"`), separate from the root's `vX.Y.Z`. A service
  requires `github.com/billz-2/packages/delivery vX.Y.Z`; the root module does not include this directory.
- Its dependencies are kept at the lowest versions that work, those of the root module and of inventory, so
  that taking it upgrades nothing in a service that already uses the root module. Raise one only when the
  code needs it, and say so in the release.
- v0: the API may still change between minor versions; a change of an exported name, a default, a table's
  DDL or a metric's name is listed in the tag message. A template change also needs a new migration in
  every service (see Migrations).
- The relay's lock key and session name, and the inbox's row key, are part of the contract between versions:
  pods of two versions run side by side during a deploy.
