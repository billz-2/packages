package outbox

// Options configure an outbox. Their zero values are the defaults.
type Options struct {
	Service     string  // required: the default lock key and session name of the relay derive from it
	Table       string  // default "outbox"
	FailedTable string  // default "outbox_failed": the records that can never be sent
	Logger      Logger  // default slog.Default()
	Metrics     Metrics // default none
}

func (o Options) withDefaults() Options {
	if o.Table == "" {
		o.Table = "outbox"
	}
	if o.FailedTable == "" {
		o.FailedTable = "outbox_failed"
	}

	return o
}
