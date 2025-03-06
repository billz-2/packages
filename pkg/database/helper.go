package database

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func ReplaceSQL(old, searchPattern string) string {
	tmpCount := strings.Count(old, searchPattern)
	for m := 1; m <= tmpCount; m++ {
		old = strings.Replace(old, searchPattern, "$"+strconv.Itoa(m), 1)
	}
	return old
}

func AttachDatabase(database, toTable, inSql string) string {
	comma := "."
	if len(database) == 0 {
		comma = ""
	}
	fullTableName := fmt.Sprintf("%s%s%s", database, comma, toTable)
	return strings.Replace(inSql, toTable, fullTableName, -1)
}

func GetCleanQuery(query string) string {
	return fmt.Sprintf("%q", regexp.MustCompile(`\s+`).ReplaceAllString(query, " "))
}
