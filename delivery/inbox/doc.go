// Package inbox records the Kafka messages a service consumed in PostgreSQL, so that a message delivered
// again is skipped, a failing one is tried again in process and then kept as a dead letter, and its offset is
// marked only once it is recorded.
//
// # The transaction of a message
//
// With InTx a handler runs in the transaction that records its message. The inbox begins it with the
// service's TxStart and gives it to the handler explicitly, as Tx(ctx). It swaps no pool and no connection:
// a statement on any other connection runs outside the message's transaction and commits on its own.
//
// A handler reports a failure in one of three ways:
//   - it returns an error: the attempt is rolled back and runs again by its RetryPolicy, unless the error is
//     Permanent; after the last attempt the message is a dead letter. Return only the errors of a transient
//     cause (Transient: a lost connection, a timeout, a deadlock or serialization failure, too many
//     connections, a server shutting down), which another attempt can get past;
//   - it returns nil and writes its failure answer with OnRow: the message is done. When one of its
//     statements failed and aborted the transaction, its own work is rolled back first;
//   - it returns Abort(err) when none of its work may commit, whatever it answered: its work is rolled back
//     and the message is done with its failure writes (OnFailedRow, else OnRow).
//
// A handler whose signature cannot return the error, as a service's response-returning handler, leaves the
// cause in its context with SetCause, and the code that calls it reads it with the func of WithCause and
// returns it.
//
// A minimal handler:
//
//	f := in.InTx("stock.reserve", begin, func(ctx context.Context, m *inbox.Message) error {
//		tx, _ := inbox.Tx(ctx)
//		_, err := tx.ExecContext(ctx, `UPDATE stock SET reserved = reserved + 1 WHERE id = $1`, string(m.Value))
//		switch {
//		case inbox.Transient(err):
//			return err // rolled back, run again
//		case err != nil:
//			inbox.OnRow(ctx, answer("reserve.failed")) // done with the failure answer, nothing reserved
//			return nil
//		}
//		inbox.OnRow(ctx, answer("reserved")) // commits with the reservation and the message's row
//		return nil
//	}, inbox.DefaultRetry)
//
// where begin is a TxStart such as
//
//	func(ctx context.Context) (*sql.Tx, func(), error) {
//		tx, err := db.BeginTx(ctx, nil)
//		return tx, nil, err
//	}
package inbox
