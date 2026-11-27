package outbox

import (
	"strconv"
	"strings"
)

var arrayElement = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// textArray and int64Array write array literals that the queries cast server-side ($1::text[]), so that no
// driver's array type is needed.
func textArray(values []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(arrayElement.Replace(v))
		b.WriteByte('"')
	}
	b.WriteByte('}')

	return b.String()
}

func int64Array(values []int64) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(v, 10))
	}
	b.WriteByte('}')

	return b.String()
}
