package mock_database

import _ "go.uber.org/mock/gomock"

//go:generate mockgen -destination contract.go -package mock_database github.com/billz-2/packages/pkg/database Tx,DB
