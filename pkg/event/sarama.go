package event

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"time"

	"github.com/IBM/sarama"
	"github.com/billz-2/packages/pkg/logger"
)

func initSaramaConfig(ctx context.Context, cfg Config) *sarama.Config {
	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V3_7_0_0
	saramaConfig.Metadata.AllowAutoTopicCreation = true
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetOldest

	applyHeartbeatInterval(saramaConfig, cfg.HeartbeatInterval)
	applySessionTimeout(saramaConfig, cfg.SessionTimeout)
	applyRebalanceTimeout(saramaConfig, cfg.RebalanceTimeout)
	applySASLPlain(ctx, saramaConfig, cfg.Username, cfg.Password)
	applySASLSSL(ctx, saramaConfig, cfg.ServerCertificate)
	return saramaConfig
}

func applyHeartbeatInterval(saramaConfig *sarama.Config, value int) {
	if value == 0 {
		return
	}
	saramaConfig.Consumer.Group.Heartbeat.Interval = time.Duration(value) * time.Second
}

func applySessionTimeout(saramaConfig *sarama.Config, value int) {
	if value == 0 {
		return
	}
	saramaConfig.Consumer.Group.Session.Timeout = time.Duration(value) * time.Second
}

func applyRebalanceTimeout(saramaConfig *sarama.Config, value int) {
	if value == 0 {
		return
	}
	saramaConfig.Consumer.Group.Rebalance.Timeout = time.Duration(value) * time.Second
}

func applySASLPlain(ctx context.Context, saramaConfig *sarama.Config, username, password string) {
	if username == "" && password == "" {
		return
	}
	saramaConfig.Net.SASL.Enable = true
	saramaConfig.Net.SASL.User = username
	saramaConfig.Net.SASL.Password = password
	saramaConfig.Net.SASL.Handshake = true
	saramaConfig.Net.SASL.Mechanism = sarama.SASLTypePlaintext
	saramaConfig.Net.TLS.Enable = true
	logger.Log.InfoWithCtx(ctx, "Applied SASLPlain auth to Kafka connection")
}

func applySASLSSL(ctx context.Context, saramaConfig *sarama.Config, serverCertificateBase64 string) {
	if serverCertificateBase64 == "" {
		return
	}
	certBytes, err := base64.StdEncoding.DecodeString(serverCertificateBase64)
	if err != nil {
		logger.Log.Error("Error while decoding cert", logger.Error(err))
		panic(err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(certBytes)
	tlsConfig := &tls.Config{
		RootCAs: caCertPool,
	}
	saramaConfig.Net.TLS.Enable = true
	saramaConfig.Net.TLS.Config = tlsConfig
	logger.Log.InfoWithCtx(ctx, "Applied SASL_SSL auth to Kafka connection")
}
