package testutil

import (
	"fmt"
	"strings"
	"time"
)

// ConceptEntryJSON emits one XBRL concept entry.
func ConceptEntryJSON(start, end, form, filed string, val float64) string {
	return fmt.Sprintf(`{"start":"%s","end":"%s","form":"%s","filed":"%s","val":%g}`,
		start, end, form, filed, val)
}

// ConceptJSON wraps entries in the companyconcept envelope.
func ConceptJSON(unit string, entries ...string) string {
	return fmt.Sprintf(`{"units":{"%s":[%s]}}`, unit, strings.Join(entries, ","))
}

func MustDate(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}
