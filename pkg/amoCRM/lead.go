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
		a.logger.WarnWithCtx(ctx, "batch update called with empty leads slice")
		return nil
	}

	a.logger.DebugWithCtx(ctx, "starting batch update for leads",
		logger.Int("lead_count", len(leads)))

	body, err := json.Marshal(leads)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "failed to marshal leads for batch update",
			logger.Error(err),
			logger.Int("lead_count", len(leads)))
		return fmt.Errorf("marshal leads: %w", err)
	}

	resp, err := a.api.patch(ctx, leadsEndpoint, nil, nil, strings.NewReader(string(body)))
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "failed to send batch update request to amoCRM",
			logger.Error(err),
			logger.Int("lead_count", len(leads)))
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
		a.logger.ErrorWithCtx(ctx, "batch update failed with unexpected status code",
			logger.Int("status_code", resp.StatusCode),
			logger.String("response_body", string(respBody)),
			logger.Int("lead_count", len(leads)))
		return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(respBody))
	}

	a.logger.DebugWithCtx(ctx, "batch update completed successfully",
		logger.Int("lead_count", len(leads)),
		logger.Int("status_code", resp.StatusCode))

	return nil
}

func (a *leads) Update(ctx context.Context, lead *LeadUpdate) (err error) {
	if lead == nil {
		a.logger.WarnWithCtx(ctx, "update called with nil lead")
		return fmt.Errorf("lead is nil")
	}

	a.logger.DebugWithCtx(ctx, "starting lead update", logger.Any("lead", lead))

	body, err := json.Marshal(lead)
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "failed to marshal lead for update",
			logger.Error(err),
			logger.Int("lead_id", lead.ID))
		return fmt.Errorf("marshal lead: %w", err)
	}

	endpoint := endpoint(fmt.Sprintf("%s/%d", leadsEndpoint, lead.ID))
	a.logger.DebugWithCtx(ctx, "sending update request to amoCRM",
		logger.Int("lead_id", lead.ID),
		logger.String("endpoint", string(endpoint)),
		logger.Int("payload_size_bytes", len(body)))

	resp, err := a.api.patch(ctx, endpoint, nil, nil, strings.NewReader(string(body)))
	if err != nil {
		a.logger.ErrorWithCtx(ctx, "failed to send update request to amoCRM",
			logger.Error(err),
			logger.Int("lead_id", lead.ID))
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
		a.logger.ErrorWithCtx(ctx, "lead update failed with unexpected status code",
			logger.Int("status_code", resp.StatusCode),
			logger.String("response_body", string(respBody)),
			logger.Int("lead_id", lead.ID))
		return fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(respBody))
	}

	a.logger.DebugWithCtx(ctx, "lead update completed successfully",
		logger.Int("lead_id", lead.ID),
		logger.Int("status_code", resp.StatusCode))

	return nil
}
