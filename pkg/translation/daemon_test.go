package translation_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/billz-2/packages/pkg/logger"
	"github.com/billz-2/packages/pkg/translation"
)

func TestDaemonSyncOnce(t *testing.T) {
	testSuit := []struct {
		name       string
		fileName   string
		force      bool
		mockFunc   func(t *testing.T, publisher *MockPublisher)
		assertFunc func(t *testing.T, published bool, err error)
	}{
		{
			name:     "unsaved own row is published and marked saved",
			fileName: "daemon_unsaved.sql",
			mockFunc: func(t *testing.T, publisher *MockPublisher) {
				publisher.EXPECT().
					PushWithKey("v1.svc_self.translations.updated", gomock.Any(), "svc_self").
					DoAndReturn(func(_ string, e cloudevents.Event, _ string) error {
						assert.Equal(t, "svc_self", e.Source())

						var msg translation.Message
						require.NoError(t, json.Unmarshal(e.Data(), &msg))
						assert.Equal(t, "svc_self", msg.ServiceName)
						assert.EqualValues(t, 1, msg.Version)
						assert.JSONEq(t, `{"a":"а"}`, string(msg.Ru))
						assert.JSONEq(t, `{"a":"a"}`, string(msg.En))
						assert.JSONEq(t, `{"a":"a"}`, string(msg.Uz))

						return nil
					}).Times(1)
			},
			assertFunc: func(t *testing.T, published bool, err error) {
				require.NoError(t, err)
				require.True(t, published)

				var saved bool
				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT saved, version FROM translations WHERE service_name = 'svc_self'`).Scan(&saved, &version))
				assert.True(t, saved)
				assert.EqualValues(t, 1, version)
			},
		},
		{
			name:     "foreign row is never published",
			fileName: "daemon_unsaved.sql",
			force:    true,
			mockFunc: func(t *testing.T, publisher *MockPublisher) {
				publisher.EXPECT().
					PushWithKey("v1.svc_self.translations.updated", gomock.Any(), "svc_self").
					Return(nil).Times(1)
			},
			assertFunc: func(t *testing.T, published bool, err error) {
				require.NoError(t, err)
				require.True(t, published)

				var saved bool
				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT saved, version FROM translations WHERE service_name = 'svc_other'`).Scan(&saved, &version))
				assert.True(t, saved)
				assert.EqualValues(t, 7, version)
			},
		},
		{
			name:     "saved row without force is skipped",
			fileName: "daemon_saved.sql",
			mockFunc: func(*testing.T, *MockPublisher) {},
			assertFunc: func(t *testing.T, published bool, err error) {
				require.NoError(t, err)
				assert.False(t, published)
			},
		},
		{
			name:     "heartbeat republishes saved row",
			fileName: "daemon_saved.sql",
			force:    true,
			mockFunc: func(t *testing.T, publisher *MockPublisher) {
				publisher.EXPECT().
					PushWithKey("v1.svc_self.translations.updated", gomock.Any(), "svc_self").
					Return(nil).Times(1)
			},
			assertFunc: func(t *testing.T, published bool, err error) {
				require.NoError(t, err)
				assert.True(t, published)
			},
		},
		{
			name:     "publish error keeps row unsaved",
			fileName: "daemon_unsaved.sql",
			mockFunc: func(t *testing.T, publisher *MockPublisher) {
				publisher.EXPECT().
					PushWithKey("v1.svc_self.translations.updated", gomock.Any(), "svc_self").
					Return(errors.New("kafka down")).Times(1)
			},
			assertFunc: func(t *testing.T, published bool, err error) {
				require.Error(t, err)
				assert.False(t, published)

				var saved bool
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT saved FROM translations WHERE service_name = 'svc_self'`).Scan(&saved))
				assert.False(t, saved)
			},
		},
		{
			name:     "row changed during publish stays unsaved",
			fileName: "daemon_unsaved.sql",
			mockFunc: func(t *testing.T, publisher *MockPublisher) {
				publisher.EXPECT().
					PushWithKey("v1.svc_self.translations.updated", gomock.Any(), "svc_self").
					DoAndReturn(func(string, cloudevents.Event, string) error {
						_, err := db.ExecContext(ctx,
							`UPDATE translations SET en = en || '{"k":"v"}' WHERE service_name = 'svc_self'`)
						require.NoError(t, err)

						return nil
					}).Times(1)
			},
			assertFunc: func(t *testing.T, published bool, err error) {
				require.NoError(t, err)
				assert.True(t, published)

				var saved bool
				var version int64
				require.NoError(t, db.QueryRowContext(ctx,
					`SELECT saved, version FROM translations WHERE service_name = 'svc_self'`).Scan(&saved, &version))
				assert.False(t, saved)
				assert.EqualValues(t, 2, version)
			},
		},
		{
			name:     "second daemon skips while lock is held",
			fileName: "daemon_unsaved.sql",
			mockFunc: func(t *testing.T, publisher *MockPublisher) {
				lockTx := db.MustBegin()
				_, err := lockTx.Exec(`SELECT pg_advisory_xact_lock(hashtext('translations_sync:svc_self'))`)
				require.NoError(t, err)

				t.Cleanup(func() { _ = lockTx.Rollback() })
			},
			assertFunc: func(t *testing.T, published bool, err error) {
				require.NoError(t, err)
				assert.False(t, published)
			},
		},
	}

	for _, ts := range testSuit {
		t.Run(ts.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			publisher := NewMockPublisher(ctrl)

			sqlBytes, err := os.ReadFile("testdata/" + ts.fileName)
			require.NoError(t, err)

			_, err = db.ExecContext(ctx, string(sqlBytes))
			require.NoError(t, err)

			ts.mockFunc(t, publisher)

			daemon, err := translation.NewDaemon(translation.DaemonConfig{
				DB:          db,
				Publisher:   publisher,
				ServiceName: "svc_self",
				Logger:      logger.New(logger.LevelError, "test"),
			})
			require.NoError(t, err)

			published, err := daemon.SyncOnce(ctx, ts.force)
			ts.assertFunc(t, published, err)
		})
	}
}

func TestNewDaemonValidation(t *testing.T) {
	testSuit := []struct {
		name         string
		serviceName  string
		nilDB        bool
		nilPublisher bool
		nilLogger    bool
		wantErr      error
	}{
		{name: "empty service name", wantErr: translation.ErrEmptyServiceName},
		{name: "nil db", serviceName: "svc_self", nilDB: true, wantErr: translation.ErrNilDB},
		{name: "nil publisher", serviceName: "svc_self", nilPublisher: true, wantErr: translation.ErrNilPublisher},
		{name: "nil logger", serviceName: "svc_self", nilLogger: true, wantErr: translation.ErrNilLogger},
	}

	for _, ts := range testSuit {
		t.Run(ts.name, func(t *testing.T) {
			config := translation.DaemonConfig{
				DB:          db,
				Publisher:   NewMockPublisher(gomock.NewController(t)),
				ServiceName: ts.serviceName,
				Logger:      logger.New(logger.LevelError, "test"),
			}

			if ts.nilDB {
				config.DB = nil
			}

			if ts.nilPublisher {
				config.Publisher = nil
			}

			if ts.nilLogger {
				config.Logger = nil
			}

			_, err := translation.NewDaemon(config)
			assert.ErrorIs(t, err, ts.wantErr)
		})
	}
}
