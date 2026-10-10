package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Detect the observed parameter-leak signature without guessing the intended
// arguments. Requiring a declared, absent sibling avoids rejecting ordinary XML
// or source content that merely mentions parameter tags.
var leakedParamRe = regexp.MustCompile(`</[A-Za-z_][\w.:-]*>[ \t\r\n]*<(?:[A-Za-z_][\w.-]*:)?parameter[ \t]+name="([^"]+)"[ \t]*>`)

func rejectLeakedArgs(raw string, schema any) error {
	if !strings.Contains(raw, "parameter") {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil // Ordinary schema validation reports malformed JSON.
	}
	root, _ := toolSchemaMap(schema)
	props, _ := root["properties"].(map[string]any)
	for key, value := range args {
		text, ok := value.(string)
		if !ok {
			continue
		}
		for _, match := range leakedParamRe.FindAllStringSubmatch(text, -1) {
			name := match[1]
			if _, declared := props[name]; declared && name != key && args[name] == nil {
				return fmt.Errorf("possible XML parameter leak in %q: resend the call with %q as a separate JSON property, not markup inside a string; no tool was executed", key, name)
			}
		}
	}
	return nil
}
