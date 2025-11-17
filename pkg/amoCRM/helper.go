package amocrm

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// GetLeadIDFromURL extracts the lead ID from an AmoCRM URL
// Supports URLs like: https://billz.amocrm.ru/leads/detail/27207417
//
// Example usage:
//   id, err := amocrm.GetLeadIDFromURL("https://billz.amocrm.ru/leads/detail/27207417")
//   if err != nil {
//       log.Fatal(err)
//   }
//   fmt.Printf("Lead ID: %d\n", id) // Output: Lead ID: 27207417
func GetLeadIDFromURL(urlStr string) (int, error) {
	if urlStr == "" {
		return 0, fmt.Errorf("URL cannot be empty")
	}

	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return 0, fmt.Errorf("invalid URL: %w", err)
	}

	// Extract the path and split it
	pathParts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")

	// Expected format: /leads/detail/{id}
	if len(pathParts) < 3 {
		return 0, fmt.Errorf("URL path does not contain enough segments")
	}

	// Find the leads segment and extract ID from the next segments
	for i, part := range pathParts {
		if part == "leads" && i+2 < len(pathParts) {
			if pathParts[i+1] == "detail" {
				leadIDStr := pathParts[i+2]
				leadID, err := strconv.Atoi(leadIDStr)
				if err != nil {
					return 0, fmt.Errorf("invalid lead ID format: %w", err)
				}
				return leadID, nil
			}
		}
	}

	// Fallback: try to get the last segment as ID if it's numeric
	lastSegment := path.Base(parsedURL.Path)
	leadID, err := strconv.Atoi(lastSegment)
	if err != nil {
		return 0, fmt.Errorf("could not extract lead ID from URL: %s", urlStr)
	}

	return leadID, nil
}