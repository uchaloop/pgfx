package pgfx

import "strings"

var likeEscaper = strings.NewReplacer(
	`\`,
	`\\`,
	`%`,
	`\%`,
	`_`,
	`\_`,
)

// EscapeLike escapes backslash, percent and underscore for literal matching
// inside a LIKE or ILIKE pattern using PostgreSQL's default backslash escape.
// Add any intended wildcards after escaping and pass the pattern as a query
// parameter. This is not SQL quoting and does not escape SIMILAR TO or regex.
// It is not compatible with a different ESCAPE character or disabled escaping.
func EscapeLike(value string) string {
	return likeEscaper.Replace(value)
}
