# amoCRM Mock Package

This package provides mock implementations of the amoCRM interfaces for testing purposes.

## Available Mocks

- `MockClient`: Mock implementation of the `amocrm.Client` interface
- `MockLeads`: Mock implementation of the `amocrm.Leads` interface
- `MockToken`: Mock implementation of the `amocrm.Token` interface

## Usage

### Setup

```
import (
    "testing"

    amocrm "github.com/billz-2/packages/pkg/amoCRM"
    mock_amocrm "github.com/billz-2/packages/pkg/amoCRM/mock"
    "go.uber.org/mock/gomock"
)

func TestSomething(t *testing.T) {
    ctrl := gomock.NewController(t)
    defer ctrl.Finish()

    // Create mock instances
    mockClient := mock_amocrm.NewMockClient(ctrl)
    mockLeads := mock_amocrm.NewMockLeads(ctrl)
    mockToken := mock_amocrm.NewMockToken(ctrl)

    // Set up expectations
    mockClient.EXPECT().Leads().Return(mockLeads)
    mockClient.EXPECT().SetDomain("test.amocrm.ru").Return(nil)
    mockToken.EXPECT().AccessToken().Return("test-token")
    mockToken.EXPECT().Expired().Return(false)
    mockClient.EXPECT().SetToken(mockToken).Return(nil)

    // Test your code that uses the amoCRM client
    // ...
}
```

### Example: Testing a Function that Uses amoCRM Client

```
// Function to test
func UpdateLeadStatus(client amocrm.Client, leadID int, statusID int) error {
    leads := client.Leads()

    lead := &amocrm.LeadUpdate{
        ID:       leadID,
        StatusID: &statusID,
    }

    return leads.Update(lead)
}

// Test
func TestUpdateLeadStatus(t *testing.T) {
    ctrl := gomock.NewController(t)
    defer ctrl.Finish()

    mockClient := mock_amocrm.NewMockClient(ctrl)
    mockLeads := mock_amocrm.NewMockLeads(ctrl)

    leadID := 12345
    statusID := 67890

    // Set up expectations
    mockClient.EXPECT().Leads().Return(mockLeads)
    mockLeads.EXPECT().Update(gomock.Any()).DoAndReturn(
        func(lead *amocrm.LeadUpdate) error {
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
```

## Regenerating Mocks

The mocks in this package are generated using [mockgen](https://github.com/uber-go/mock). To regenerate them, run:

```
go generate ./...
```

This will execute the go:generate directives in the mock.go file.
