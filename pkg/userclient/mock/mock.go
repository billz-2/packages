package mock_userclient

import _ "go.uber.org/mock/mockgen/model"

//go:generate go run go.uber.org/mock/mockgen -destination mock_client.go -package mock_userclient github.com/billz-2/packages/pkg/userclient Client
