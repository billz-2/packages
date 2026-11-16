package translation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	cloudevents "github.com/cloudevents/sdk-go/v2"

	"github.com/billz-2/packages/pkg/logger"
)

// Execer is the minimal write interface (*sqlx.DB, *sqlx.Tx, etc.).
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ConsumerConfig holds the consumer handler parameters.
type ConsumerConfig struct {
	DB          Execer
	ServiceName string
	Logger      logger.Logger
}

func (c ConsumerConfig) validate() error {
	switch {
	case c.ServiceName == "":
		return ErrEmptyServiceName
	case c.DB == nil:
		return ErrNilDB
	case c.Logger == nil:
		return ErrNilLogger
	}

	return nil
}

// ConsumerHandler stores dictionaries of other services received via Kafka.
type ConsumerHandler struct {
	db          Execer
	serviceName string
	log         logger.Logger
}

// NewConsumerHandler creates a consumer handler.
func NewConsumerHandler(cfg ConsumerConfig) (*ConsumerHandler, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &ConsumerHandler{db: cfg.DB, serviceName: cfg.ServiceName, log: cfg.Logger}, nil
}

// Handle processes an event. It returns nil for processed and skipped messages;
// a database error is wrapped into ErrRetry. Broken messages are only logged:
// a retry on them would stall the whole consumer group.
func (h *ConsumerHandler) Handle(ctx context.Context, e cloudevents.Event) error {
	var msg Message
	if err := json.Unmarshal(e.Data(), &msg); err != nil {
		h.skip("decode message", e, err)
		return nil
	}

	if msg.ServiceName == h.serviceName {
		return nil
	}

	if reason := h.validate(e, msg); reason != "" {
		h.skip(reason, e, nil)
		return nil
	}

	// The upsert is atomic: a row is overwritten only if the incoming version is newer.
	if _, err := h.db.ExecContext(ctx, `
INSERT INTO translations (service_name, ru, en, uz, saved, version, updated_at)
VALUES ($1, $2, $3, $4, true, $5, now())
ON CONFLICT (service_name) DO UPDATE
SET ru = EXCLUDED.ru, en = EXCLUDED.en, uz = EXCLUDED.uz, saved = true, version = EXCLUDED.version, updated_at = now()
WHERE translations.version < EXCLUDED.version`,
		msg.ServiceName, []byte(msg.Ru), []byte(msg.En), []byte(msg.Uz), msg.Version); err != nil {
		return fmt.Errorf("%w: %w", ErrRetry, err)
	}

	return nil
}

// validate returns the reason why the message must be skipped, or an empty string if it is valid.
func (h *ConsumerHandler) validate(e cloudevents.Event, msg Message) string {
	switch {
	case msg.ServiceName == "":
		return "empty service name"
	case e.Source() != msg.ServiceName:
		return "source mismatch"
	case msg.Version <= 0:
		return "non positive version"
	case !isStringDict(msg.Ru) || !isStringDict(msg.En) || !isStringDict(msg.Uz):
		return "dictionary is not a json object of strings"
	}

	return ""
}

func (h *ConsumerHandler) skip(reason string, e cloudevents.Event, err error) {
	fields := []logger.Field{
		logger.String("reason", reason),
		logger.String("source", e.Source()),
		logger.String("event_id", e.ID()),
	}

	if err != nil {
		fields = append(fields, logger.Error(err))
	}

	h.log.Warn("translations message skipped", fields...)
}

func isStringDict(raw json.RawMessage) bool {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return false
	}

	var dict map[string]string

	return json.Unmarshal(raw, &dict) == nil
}
