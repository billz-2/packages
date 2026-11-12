package translation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/billz-2/packages/pkg/logger"
)

const (
	// DefaultPollInterval is the default period of checking for an unsaved row.
	DefaultPollInterval = time.Hour
	// DefaultHeartbeatInterval is the default period of the forced publication.
	DefaultHeartbeatInterval = 24 * time.Hour

	// maxMessageSize is the message size threshold above which a warning is logged.
	maxMessageSize = 700 * 1024
)

// Publisher sends an event to Kafka with the given key.
//
//go:generate mockgen -source=daemon.go -destination=publisher_mock_test.go -package=translation_test
type Publisher interface {
	PushWithKey(topic string, e cloudevents.Event, key string) error
}

// DaemonConfig holds the publishing daemon parameters.
type DaemonConfig struct {
	DB                *sqlx.DB
	Publisher         Publisher
	ServiceName       string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	Logger            logger.Logger
}

// Daemon publishes the own translations row of the service.
type Daemon struct {
	db                *sqlx.DB
	publisher         Publisher
	serviceName       string
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	log               logger.Logger
}

func (c DaemonConfig) validate() error {
	switch {
	case c.ServiceName == "":
		return ErrEmptyServiceName
	case c.DB == nil:
		return ErrNilDB
	case c.Publisher == nil:
		return ErrNilPublisher
	case c.Logger == nil:
		return ErrNilLogger
	}

	return nil
}

// NewDaemon creates a daemon; zero intervals are replaced with the defaults.
func NewDaemon(cfg DaemonConfig) (*Daemon, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}

	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = DefaultHeartbeatInterval
	}

	return &Daemon{
		db:                cfg.DB,
		publisher:         cfg.Publisher,
		serviceName:       cfg.ServiceName,
		pollInterval:      cfg.PollInterval,
		heartbeatInterval: cfg.HeartbeatInterval,
		log:               cfg.Logger,
	}, nil
}

// SyncOnce publishes the service row if it is not saved (or force=true).
func (d *Daemon) SyncOnce(ctx context.Context, force bool) (published bool, err error) {
	tx, err := d.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	var locked bool
	if err = tx.GetContext(ctx, &locked,
		`SELECT pg_try_advisory_xact_lock(hashtext($1))`, "translations_sync:"+d.serviceName); err != nil {
		return false, fmt.Errorf("advisory lock: %w", err)
	}

	if !locked {
		return false, nil
	}

	msg := Message{ServiceName: d.serviceName}

	err = tx.QueryRowContext(ctx,
		`SELECT version, ru, en, uz FROM translations WHERE service_name = $1 AND (saved = false OR $2)`,
		d.serviceName, force).Scan(&msg.Version, &msg.Ru, &msg.En, &msg.Uz)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("select translations: %w", err)
	}

	e := cloudevents.NewEvent()
	e.SetID(uuid.NewString())
	e.SetSource(d.serviceName)
	e.SetType(Topic(d.serviceName))

	if err = e.SetData(cloudevents.ApplicationJSON, msg); err != nil {
		return false, fmt.Errorf("set event data: %w", err)
	}

	if size := len(e.Data()); size > maxMessageSize {
		d.log.Warn("translations message is large",
			logger.String("service", d.serviceName), logger.Int("bytes", size))
	}

	if err = d.publisher.PushWithKey(Topic(d.serviceName), e, d.serviceName); err != nil {
		return false, fmt.Errorf("publish: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE translations SET saved = true WHERE service_name = $1 AND version = $2`,
		d.serviceName, msg.Version)
	if err != nil {
		return false, fmt.Errorf("mark saved: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		d.log.Info("translations row changed during publish, will be sent on next pass",
			logger.String("service", d.serviceName), logger.Int64("version", msg.Version))
	}

	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}

	return true, nil
}

// Run makes a heartbeat right away, then polls and sends heartbeats by tickers until ctx is canceled.
func (d *Daemon) Run(ctx context.Context) {
	d.syncAndLog(ctx, true)

	poll := time.NewTicker(d.pollInterval)
	defer poll.Stop()

	heartbeat := time.NewTicker(d.heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			d.syncAndLog(ctx, false)
		case <-heartbeat.C:
			d.syncAndLog(ctx, true)
		}
	}
}

func (d *Daemon) syncAndLog(ctx context.Context, force bool) {
	if _, err := d.SyncOnce(ctx, force); err != nil {
		if ctx.Err() != nil {
			return
		}

		d.log.Error("translations sync failed",
			logger.String("service", d.serviceName), logger.Bool("force", force), logger.Error(err))
	}
}
