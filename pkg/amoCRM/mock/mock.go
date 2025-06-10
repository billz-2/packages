package mock_amocrm

import _ "go.uber.org/mock/mockgen/model"

//go:generate go run go.uber.org/mock/mockgen -destination client.go -package mock_amocrm github.com/billz-2/packages/pkg/amoCRM Client
//go:generate go run go.uber.org/mock/mockgen -destination leads.go -package mock_amocrm github.com/billz-2/packages/pkg/amoCRM Leads
//go:generate go run go.uber.org/mock/mockgen -destination token.go -package mock_amocrm github.com/billz-2/packages/pkg/amoCRM Token
