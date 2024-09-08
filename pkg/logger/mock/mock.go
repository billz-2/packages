package mock_logger

import _ "github.com/golang/mock/mockgen/model"

//go:generate mockgen -destination logger.go -package mock_logger gitlab.udevs.io/billz/billz_inventory_service_v2/pkg/logger Logger
