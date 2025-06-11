package amocrm

import (
	"fmt"
)

type endpoint string

func (e endpoint) path() string {
	return fmt.Sprintf("/api/v%d/%s", apiVersion, e)
}

const (
	leadsEndpoint endpoint = "leads"
)
