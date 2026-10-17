package install

import (
	"encoding/json"
	"strings"
)

// jsonText writes v the way the commands always embedded it: compact, with <, > and & as they are. A nil list is the empty list.
func jsonText(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic("install: " + err.Error()) // only strings, bools, numbers and lists of them are ever given
	}
	return strings.TrimSuffix(b.String(), "\n")
}
