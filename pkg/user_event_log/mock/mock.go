package mock_usereventlog

import _ "github.com/golang/mock/mockgen/model"

//go:generate mockgen -destination kafka.go -package mock_usereventlog github.com/billz-2/packages/pkg/user_event_log Kafka