package mock_amocrm_test

import (
	"context"
	"testing"

	amocrm "github.com/billz-2/packages/pkg/amoCRM"
	mock_amocrm "github.com/billz-2/packages/pkg/amoCRM/mock"
	"go.uber.org/mock/gomock"
)

const BearerPrefix = "Bearer"

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

// TestSimpleClientWorkflow demonstrates a simple workflow with the mock client
func TestSimpleClientWorkflow(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock instances
	mockClient := mock_amocrm.NewMockClient(ctrl)
	mockLeads := mock_amocrm.NewMockLeads(ctrl)

	// Set up client expectations
	mockClient.EXPECT().Leads().Return(mockLeads).AnyTimes()

	// Set up leads expectations
	leadID := 12345
	leadUpdate := &amocrm.LeadUpdate{
		ID:       leadID,
		StatusID: &[]int{100}[0], // Convert to pointer
	}
	mockLeads.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

	// Use the mocks
	leads := mockClient.Leads()
	err := leads.Update(ctx, leadUpdate)
	if err != nil {
		t.Fatalf("Failed to update lead: %v", err)
	}
}

// TestBatchUpdateWorkflow demonstrates batch update with the mock client
func TestBatchUpdateWorkflow(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock instances
	mockClient := mock_amocrm.NewMockClient(ctrl)
	mockLeads := mock_amocrm.NewMockLeads(ctrl)

	// Set up client expectations
	mockClient.EXPECT().Leads().Return(mockLeads)

	// Create test leads
	leads := []*amocrm.LeadUpdate{
		{ID: 1, StatusID: &[]int{100}[0]},
		{ID: 2, StatusID: &[]int{200}[0]},
	}

	// Set up batch update expectation
	mockLeads.EXPECT().BatchUpdate(gomock.Any(), gomock.Eq(leads)).Return(nil)

	// Use the mocks
	leadsClient := mockClient.Leads()
	err := leadsClient.BatchUpdate(ctx, leads)
	if err != nil {
		t.Fatalf("Failed to batch update leads: %v", err)
	}
}
