package mock_amocrm_test

import (
	"context"
	"testing"
	"time"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	mock_amocrm "github.com/billz-2/packages/pkg/amoCRM/mock"
	"go.uber.org/mock/gomock"
)

var ctx = context.Background()

// UpdateLeadStatus is an example function that uses the amoCRM client
func UpdateLeadStatus(client amocrm.Client, leadID int, statusID int) error {
	leads := client.Leads()

	lead := &amocrm.LeadUpdate{
		ID:       leadID,
		StatusID: &statusID,
	}

	return leads.Update(ctx, lead)
}

// TestUpdateLeadStatus demonstrates how to use the mock client
func TestUpdateLeadStatus(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mock_amocrm.NewMockClient(ctrl)
	mockLeads := mock_amocrm.NewMockLeads(ctrl)

	leadID := 12345
	statusID := 67890

	// Set up expectations
	mockClient.EXPECT().Leads().Return(mockLeads)
	mockLeads.EXPECT().Update(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, lead *amocrm.LeadUpdate) error {
			// Verify the lead has the correct ID and status
			if lead.ID != leadID {
				t.Errorf("Expected lead ID %d, got %d", leadID, lead.ID)
			}
			if *lead.StatusID != statusID {
				t.Errorf("Expected status ID %d, got %d", statusID, *lead.StatusID)
			}
			return nil
		},
	)

	// Call the function being tested
	err := UpdateLeadStatus(mockClient, leadID, statusID)

	// Assert the result
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}

// TestCompleteWorkflow demonstrates a more complete workflow with the mock client
func TestCompleteWorkflow(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock instances
	mockClient := mock_amocrm.NewMockClient(ctrl)
	mockLeads := mock_amocrm.NewMockLeads(ctrl)
	mockToken := mock_amocrm.NewMockToken(ctrl)

	// Set up token expectations
	mockToken.EXPECT().AccessToken().Return("test-access-token").AnyTimes()
	mockToken.EXPECT().RefreshToken().Return("test-refresh-token").AnyTimes()
	mockToken.EXPECT().TokenType().Return("Bearer").AnyTimes()
	mockToken.EXPECT().ExpiresAt().Return(time.Now().Add(1 * time.Hour)).AnyTimes()
	mockToken.EXPECT().Expired().Return(false).AnyTimes()

	// Set up client expectations
	mockClient.EXPECT().SetDomain(gomock.Any(), "test.amocrm.ru").Return(nil)
	mockClient.EXPECT().SetToken(gomock.Any(), mockToken).Return(nil)
	mockClient.EXPECT().Leads().Return(mockLeads).AnyTimes()

	// Set up leads expectations - using Update since GetByID doesn't exist in the interface
	leadID := 12345
	leadUpdate := &amocrm.LeadUpdate{
		ID:       leadID,
		StatusID: &[]int{100}[0], // Convert to pointer
	}
	mockLeads.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

	// Use the mocks
	err := mockClient.SetDomain(ctx, "test.amocrm.ru")
	if err != nil {
		t.Fatalf("Failed to set domain: %v", err)
	}

	err = mockClient.SetToken(ctx, mockToken)
	if err != nil {
		t.Fatalf("Failed to set token: %v", err)
	}

	leads := mockClient.Leads()
	err = leads.Update(ctx, leadUpdate)
	if err != nil {
		t.Fatalf("Failed to update lead: %v", err)
	}
}
