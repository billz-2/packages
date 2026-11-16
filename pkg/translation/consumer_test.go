package translation_test

import (
	"errors"
	"os"
	"testing"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/translation"
)

func TestConsumerHandle(t *testing.T) {
	testSuit := []struct {
		name        string
		fileName    string
		serviceName string
		closedDB    bool
		source      string
		data        string
		assertFunc  func(t *testing.T, err error)
	}{
		{
			name:     "newer foreign version is upserted",
			fileName: "consumer.sql",
			source:   "svc_other",
			data:     `{"service_name":"svc_other","version":6,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var saved bool
				var version int64
				var en string
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT saved, version, en->>'a' FROM translations WHERE service_name = 'svc_other'`).
					Scan(&saved, &version, &en))
				assert.True(t, saved)
				assert.EqualValues(t, 6, version)
				assert.Equal(t, "new", en)
			},
		},
		{
			name:     "new foreign service is inserted",
			fileName: "consumer.sql",
			source:   "svc_new",
			data:     `{"service_name":"svc_new","version":1,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var saved bool
				var version int64
				var en string
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT saved, version, en->>'a' FROM translations WHERE service_name = 'svc_new'`).
					Scan(&saved, &version, &en))
				assert.True(t, saved)
				assert.EqualValues(t, 1, version)
				assert.Equal(t, "new", en)
			},
		},
		{
			name:     "unsaved foreign row becomes saved after upsert",
			fileName: "consumer_foreign_unsaved.sql",
			source:   "svc_other",
			data:     `{"service_name":"svc_other","version":8,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var saved bool
				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT saved, version FROM translations WHERE service_name = 'svc_other'`).Scan(&saved, &version))
				assert.True(t, saved)
				assert.EqualValues(t, 8, version)
			},
		},
		{
			name:     "same version is ignored",
			fileName: "consumer.sql",
			source:   "svc_other",
			data:     `{"service_name":"svc_other","version":5,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var en string
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT en->>'a' FROM translations WHERE service_name = 'svc_other'`).Scan(&en))
				assert.Equal(t, "old", en)
			},
		},
		{
			name:     "older version is ignored",
			fileName: "consumer.sql",
			source:   "svc_other",
			data:     `{"service_name":"svc_other","version":4,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var version int64
				var en string
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT version, en->>'a' FROM translations WHERE service_name = 'svc_other'`).Scan(&version, &en))
				assert.EqualValues(t, 5, version)
				assert.Equal(t, "old", en)
			},
		},
		{
			// svc_x is not the trigger argument, so without the own service check
			// in the handler the svc_x row would appear in the table.
			name:        "own service message is ignored",
			fileName:    "consumer.sql",
			serviceName: "svc_x",
			source:      "svc_x",
			data:        `{"service_name":"svc_x","version":99,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var count int
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM translations WHERE service_name = 'svc_x'`).Scan(&count))
				assert.Equal(t, 0, count)
			},
		},
		{
			name:     "empty service name is skipped",
			fileName: "consumer.sql",
			source:   "svc_other",
			data:     `{"service_name":"","version":9,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var count int
				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*), max(version) FROM translations WHERE service_name IN ('', 'svc_other')`).
					Scan(&count, &version))
				assert.Equal(t, 1, count)
				assert.EqualValues(t, 5, version)
			},
		},
		{
			name:     "non string dictionary value is skipped",
			fileName: "consumer.sql",
			source:   "svc_other",
			data:     `{"service_name":"svc_other","version":6,"ru":{"a":"ru"},"en":{"a":1},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT version FROM translations WHERE service_name = 'svc_other'`).Scan(&version))
				assert.EqualValues(t, 5, version)
			},
		},
		{
			name:     "source mismatch is skipped",
			fileName: "consumer.sql",
			source:   "svc_evil",
			data:     `{"service_name":"svc_other","version":9,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT version FROM translations WHERE service_name = 'svc_other'`).Scan(&version))
				assert.EqualValues(t, 5, version)
			},
		},
		{
			name:     "malformed payload is skipped not retried",
			fileName: "consumer.sql",
			source:   "svc_other",
			data:     `{"version":"x"}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var count int
				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*), max(version) FROM translations`).Scan(&count, &version))
				assert.Equal(t, 2, count)
				assert.EqualValues(t, 5, version)
			},
		},
		{
			name:     "non object dictionary is skipped",
			fileName: "consumer.sql",
			source:   "svc_other",
			data:     `{"service_name":"svc_other","version":6,"ru":{"a":"ru"},"en":["x"],"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT version FROM translations WHERE service_name = 'svc_other'`).Scan(&version))
				assert.EqualValues(t, 5, version)
			},
		},
		{
			name:     "non positive version is skipped",
			fileName: "consumer.sql",
			source:   "svc_new",
			data:     `{"service_name":"svc_new","version":0,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.NoError(t, err)

				var count int
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT count(*) FROM translations WHERE service_name = 'svc_new'`).Scan(&count))
				assert.Equal(t, 0, count)
			},
		},
		{
			name:     "db failure is retryable",
			fileName: "consumer.sql",
			closedDB: true,
			source:   "svc_other",
			data:     `{"service_name":"svc_other","version":6,"ru":{"a":"ru"},"en":{"a":"new"},"uz":{"a":"uz"}}`,
			assertFunc: func(t *testing.T, err error) {
				require.Error(t, err)
				assert.True(t, errors.Is(err, translation.ErrRetry))
			},
		},
	}

	for _, ts := range testSuit {
		t.Run(ts.name, func(t *testing.T) {
			sqlBytes, err := os.ReadFile("testdata/" + ts.fileName)
			require.NoError(t, err)

			_, err = db.ExecContext(ctx, string(sqlBytes))
			require.NoError(t, err)

			var execer translation.Execer = db

			if ts.closedDB {
				closed, openErr := sqlx.Open("postgres", databaseURL)
				require.NoError(t, openErr)
				require.NoError(t, closed.Close())

				execer = closed
			}

			serviceName := ts.serviceName
			if serviceName == "" {
				serviceName = "svc_self"
			}

			handler, err := translation.NewConsumerHandler(translation.ConsumerConfig{
				DB:          execer,
				ServiceName: serviceName,
				Logger:      logger.New(logger.LevelError, "test"),
			})
			require.NoError(t, err)

			event := cloudevents.NewEvent()
			event.SetID("test-id")
			event.SetSource(ts.source)
			event.SetType(translation.Topic(ts.source))
			require.NoError(t, event.SetData(cloudevents.ApplicationJSON, []byte(ts.data)))

			ts.assertFunc(t, handler.Handle(ctx, event))
		})
	}
}

func TestNewConsumerHandlerValidation(t *testing.T) {
	testSuit := []struct {
		name        string
		serviceName string
		nilDB       bool
		nilLogger   bool
		wantErr     error
	}{
		{name: "empty service name", wantErr: translation.ErrEmptyServiceName},
		{name: "nil db", serviceName: "svc_self", nilDB: true, wantErr: translation.ErrNilDB},
		{name: "nil logger", serviceName: "svc_self", nilLogger: true, wantErr: translation.ErrNilLogger},
	}

	for _, ts := range testSuit {
		t.Run(ts.name, func(t *testing.T) {
			config := translation.ConsumerConfig{
				DB:          db,
				ServiceName: ts.serviceName,
				Logger:      logger.New(logger.LevelError, "test"),
			}

			if ts.nilDB {
				config.DB = nil
			}

			if ts.nilLogger {
				config.Logger = nil
			}

			_, err := translation.NewConsumerHandler(config)
			assert.ErrorIs(t, err, ts.wantErr)
		})
	}
}
