package amocrm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/billz-2/packages/pkg/logger"
)

type Leads interface {
	BatchUpdate(ctx context.Context, leads []*LeadUpdate) (err error)
	Update(ctx context.Context, lead *LeadUpdate) (err error)
}

type leads struct {
	api    *api
	logger logger.Logger
}

func NewLead(api *api, logger logger.Logger) Leads {
	return &leads{
		api:    api,
		logger: logger,
	}
}

func (a *leads) BatchUpdate(ctx context.Context, leads []*LeadUpdate) (err error) {
	if len(leads) == 0 {
		return nil
	}

	body, err := json.Marshal(leads)
	if err != nil {
		return fmt.Errorf("marshal leads: %w", err)
	}

	resp, err := a.api.patch(ctx, leadsEndpoint, nil, nil, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("patch leads: %w", err)
	}
	defer func() {
		if clErr := resp.Body.Close(); clErr != nil {
			if err != nil {
				err = fmt.Errorf("close response body: %v: %v", clErr, err)
			} else {
				err = fmt.Errorf("close response body: %w", clErr)
			}
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (a *leads) Update(ctx context.Context, lead *LeadUpdate) (err error) {
	if lead == nil {
		return fmt.Errorf("lead is nil")
	}

	body, err := json.Marshal(lead)
	if err != nil {
		return fmt.Errorf("marshal lead: %w", err)
	}

	endpoint := endpoint(fmt.Sprintf("%s/%d", leadsEndpoint, lead.ID))
	resp, err := a.api.patch(ctx, endpoint, nil, nil, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("patch lead: %w", err)
	}
	defer func() {
		if clErr := resp.Body.Close(); clErr != nil {
			if err != nil {
				err = fmt.Errorf("close response body: %v: %v", clErr, err)
			} else {
				err = fmt.Errorf("close response body: %w", clErr)
			}
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
