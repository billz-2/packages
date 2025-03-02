package user_event_log

import "fmt"

func ToStringMap(values []any) []string {
	stringValues := make([]string, len(values))
	for i, v := range values {
		stringValues[i] = fmt.Sprint(v)
	}

	return stringValues
}
