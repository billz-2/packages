# amoCRM Mock Package

This package provides mock implementations of the amoCRM interfaces for testing purposes.

## Available Mocks

- `MockClient`: Mock implementation of the `amocrm.Client` interface
- `MockLeads`: Mock implementation of the `amocrm.Leads` interface

## Usage

### Setup

```go
import (
    "context"
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

    // Set up expectations
    mockClient.EXPECT().Leads().Return(mockLeads)

    // Test your code that uses the amoCRM client
    // ...
}
```

### Example: Testing a Function that Uses amoCRM Client

```go
// Function to test
func UpdateLeadStatus(client amocrm.Client, leadID int, statusID int) error {
    ctx := context.Background()
    leads := client.Leads()

    lead := &amocrm.LeadUpdate{
        ID:       leadID,
        StatusID: &statusID,
    }

    return leads.Update(ctx, lead)
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
```

### Example: Testing Batch Update

```go
func TestBatchUpdateLeads(t *testing.T) {
    ctrl := gomock.NewController(t)
    defer ctrl.Finish()

    mockClient := mock_amocrm.NewMockClient(ctrl)
    mockLeads := mock_amocrm.NewMockLeads(ctrl)

    // Create test leads
    leads := []*amocrm.LeadUpdate{
        {ID: 1, StatusID: &[]int{100}[0]},
        {ID: 2, StatusID: &[]int{200}[0]},
    }

    // Set up expectations
    mockClient.EXPECT().Leads().Return(mockLeads)
    mockLeads.EXPECT().BatchUpdate(gomock.Any(), gomock.Eq(leads)).Return(nil)

    // Test the batch update
    ctx := context.Background()
    leadsClient := mockClient.Leads()
    err := leadsClient.BatchUpdate(ctx, leads)

    if err != nil {
        t.Errorf("Expected no error, got %v", err)
    }
}
```

## Client Creation

The amoCRM client is created with a simplified interface:

```go
import (
    amocrm "github.com/billz-2/packages/pkg/amoCRM"
    "github.com/billz-2/packages/pkg/logger"
)

func ExampleClientCreation() {
    logger := logger.New(logger.LevelInfo, "amocrm")
    
    // Create client with: clientID, clientSecret, token, redirectURL, logger
    client := amocrm.New(
        "your-client-id",
        "your-client-secret", 
        "your-bearer-token",
        "https://your-redirect-url.com",
        logger,
    )
    
    // Use the client
    leads := client.Leads()
    // ... work with leads
}
```

## Constants

The package includes useful constants for testing:

```go
const BearerPrefix = "Bearer"
```

### Bearer Token Reference

The `BearerPrefix` constant represents the "Bearer" token type prefix used in Authorization headers. This constant should be used in the main implementation instead of hardcoded strings.

**Current implementation in `api.go`:**
```go
authHeader := "Bearer" + " " + a.token
```

**Should be changed to:**
```go
const BearerPrefix = "Bearer"
authHeader := BearerPrefix + " " + a.token
```

This constant is available in test files for consistency and to avoid magic strings in tests.

## Regenerating Mocks

The mocks in this package are generated using [mockgen](https://github.com/uber-go/mock). To regenerate them, run:

```bash
# From the project root
mockgen -destination pkg/amoCRM/mock/client.go -package mock_amocrm github.com/billz-2/packages/pkg/amoCRM Client
mockgen -destination pkg/amoCRM/mock/leads.go -package mock_amocrm github.com/billz-2/packages/pkg/amoCRM Leads
```

## Interface Changes

This package has been updated to work with the simplified amoCRM interface:

- **Removed**: Token interface and related mocking (no longer needed)
- **Simplified**: Client interface now only provides `Leads()` method
- **Updated**: Constructor takes token as string parameter instead of using SetToken method
- **Bearer Token**: Authentication is handled internally with string tokens

For examples of the updated usage patterns, see the test files in this package.