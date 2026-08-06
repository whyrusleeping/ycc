package tools

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/whyrusleeping/gollama"
)

// Models sometimes leak the XML-ish tool-invoke syntax INTO a JSON string
// argument: they close the current parameter with a tag and then write the
// remaining parameters as `<parameter name="…">value` markup *inside* the string
// they were already writing. Observed in the wild from Anthropic models on both
// `ask_user` and `create_task`:
//
//	{"question":"… What next?</question>\n<parameter name=\"options\">[\"a\",\"b\"]"}
//	{"description":"…</description>\n<parameter name=\"priority\">3", …}
//
// The call is well-formed JSON, so nothing downstream notices: the tool sees one
// giant argument, the sibling arguments are silently lost, and the user gets a
// question with raw markup in it and no option picker.
//
// RepairLeakedArgs undoes that. It is deliberately conservative — it only fires
// when BOTH signals are present:
//
//  1. the string contains a closing tag immediately followed by a
//     `<parameter name="X">` block (the signature of the leak, not of prose that
//     merely mentions the syntax), and
//  2. `X` is a parameter this tool actually declares AND is missing (or empty) in
//     the call.
//
// so a Write/create_task call whose content legitimately *documents* this syntax
// is left alone unless it happens to name that same tool's own missing parameter.
var (
	leakedParamRe = regexp.MustCompile(`</[A-Za-z_][\w.:-]*>[ \t\r\n]*<(?:[A-Za-z_][\w.-]*:)?parameter[ \t]+name="([^"]+)"[ \t]*>`)
	anyParamRe    = regexp.MustCompile(`<(?:[A-Za-z_][\w.-]*:)?parameter[ \t]+name="([^"]+)"[ \t]*>`)
	trailingTagRe = regexp.MustCompile(`[ \t\r\n]*</[A-Za-z_][\w.:-]*>[ \t\r\n]*$`)
)

// RepairLeakedArgs rewrites a tool call's raw JSON arguments, moving leaked
// `<parameter name="X">value` blocks out of a string argument and into real
// arguments. It returns the (possibly rewritten) JSON and the names recovered;
// an empty name list means nothing was changed.
func RepairLeakedArgs(raw string, declared map[string]bool) (string, []string) {
	if !strings.Contains(raw, "parameter name=") || len(declared) == 0 {
		return raw, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return raw, nil
	}
	var recovered []string
	for key, val := range args {
		host, ok := val.(string)
		if !ok {
			continue
		}
		loc := leakedParamRe.FindStringIndex(host)
		if loc == nil {
			continue
		}
		extracted := parseLeakedParams(host[loc[0]:])
		applied := 0
		for _, kv := range extracted {
			if !declared[kv.name] || kv.name == key {
				continue
			}
			if existing, ok := args[kv.name]; ok && !isEmptyArg(existing) {
				continue // never clobber an argument the model actually sent
			}
			args[kv.name] = kv.value
			recovered = append(recovered, kv.name)
			applied++
		}
		if applied == 0 {
			continue
		}
		// Cut the host string at the closing tag that started the leak.
		args[key] = strings.TrimRight(host[:loc[0]], " \t\r\n")
	}
	if len(recovered) == 0 {
		return raw, nil
	}
	fixed, err := json.Marshal(args)
	if err != nil {
		return raw, nil
	}
	return string(fixed), recovered
}

type leakedParam struct {
	name  string
	value any
}

// parseLeakedParams pulls `<parameter name="X">value` pairs out of the tail of a
// leaked string. Values are JSON-decoded when they parse (so `["a","b"]` becomes
// a real list and `3` a real number) and kept as trimmed text otherwise.
func parseLeakedParams(tail string) []leakedParam {
	matches := anyParamRe.FindAllStringSubmatchIndex(tail, -1)
	out := make([]leakedParam, 0, len(matches))
	for i, m := range matches {
		name := tail[m[2]:m[3]]
		end := len(tail)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		text := tail[m[1]:end]
		// Drop the block's own closing tag (`</parameter>`) and any wrapper the
		// model closed after it (`</invoke>`, `</function_calls>`, …).
		for {
			trimmed := trailingTagRe.ReplaceAllString(text, "")
			if trimmed == text {
				break
			}
			text = trimmed
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, leakedParam{name: name, value: decodeLeakedValue(text)})
	}
	return out
}

func decodeLeakedValue(text string) any {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err == nil {
		return v
	}
	return text
}

func isEmptyArg(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// declaredParams returns the parameter names a tool's schema declares.
func declaredParams(params any) map[string]bool {
	var props map[string]any
	switch p := params.(type) {
	case gollama.ToolFunctionParams:
		props = p.Properties
	case *gollama.ToolFunctionParams:
		if p != nil {
			props = p.Properties
		}
	case map[string]any:
		props, _ = p["properties"].(map[string]any)
	}
	if len(props) == 0 {
		return nil
	}
	names := make(map[string]bool, len(props))
	for name := range props {
		names[name] = true
	}
	return names
}

// Repair returns the call with leaked `<parameter …>` markup moved back into
// real arguments, plus the recovered names. Unknown tools and clean calls are
// returned untouched, so callers can apply it unconditionally.
func (r *Registry) Repair(call gollama.ToolCall) (gollama.ToolCall, []string) {
	t, ok := r.byName[call.Function.Name]
	if !ok {
		return call, nil
	}
	fixed, recovered := RepairLeakedArgs(call.Function.Arguments, declaredParams(t.Params))
	if len(recovered) == 0 {
		return call, nil
	}
	call.Function.Arguments = fixed
	return call, recovered
}
