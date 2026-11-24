package antigravity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// prepareInner decodes gollama's Gemini request body for use as the
// envelope's request member: it sets sessionId, converts tool schemas to the
// legacy parameters field, and reports the requested thinkingLevel.
func prepareInner(body []byte, sessionID string) (map[string]any, string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var inner map[string]any
	if err := dec.Decode(&inner); err != nil {
		return nil, "", fmt.Errorf("antigravity: decoding Gemini request: %w", err)
	}
	inner["sessionId"] = sessionID
	level := ""
	if gc, ok := inner["generationConfig"].(map[string]any); ok {
		if tc, ok := gc["thinkingConfig"].(map[string]any); ok {
			level, _ = tc["thinkingLevel"].(string)
			// oh-my-pi's live Neon run (2026-11-19) sent xhigh in lower
			// case; the other levels upper case, as gollama emits them.
			if level == "XHIGH" {
				tc["thinkingLevel"] = "xhigh"
			}
		}
	}
	if tools, ok := inner["tools"].([]any); ok {
		for _, tool := range tools {
			tm, ok := tool.(map[string]any)
			if !ok {
				continue
			}
			decls, _ := tm["functionDeclarations"].([]any)
			for _, d := range decls {
				dm, ok := d.(map[string]any)
				if !ok {
					continue
				}
				if schema, ok := dm["parametersJsonSchema"]; ok {
					delete(dm, "parametersJsonSchema")
					dm["parameters"] = sanitizeSchema(schema)
				}
			}
		}
	}
	return inner, level, nil
}

// matchThinkingLevel makes thinkingConfig.thinkingLevel agree with a wire
// model id that carries a level suffix (gemini-4-neon-low runs at LOW).
func matchThinkingLevel(inner map[string]any, wireModel string) {
	level := ""
	for _, s := range thinkingSuffixes {
		if strings.HasSuffix(wireModel, s) {
			level = strings.ToUpper(strings.TrimPrefix(s, "-"))
		}
	}
	if level == "" {
		return
	}
	if level == "XHIGH" {
		level = "xhigh"
	}
	gc, _ := inner["generationConfig"].(map[string]any)
	if gc == nil {
		gc = map[string]any{}
		inner["generationConfig"] = gc
	}
	tc, _ := gc["thinkingConfig"].(map[string]any)
	if tc == nil {
		tc = map[string]any{}
		gc["thinkingConfig"] = tc
	}
	tc["thinkingLevel"] = level
}

// schemaKeys are the keywords the legacy parameters field (an OpenAPI 3.0
// Schema subset) defines; everything else is dropped. title, default and
// example are omitted although defined, as the Antigravity cleaners in
// opencode-antigravity-auth and CLIProxyAPI drop them too.
var schemaKeys = map[string]bool{
	"type": true, "format": true, "description": true, "nullable": true, "enum": true,
	"items": true, "properties": true, "required": true, "anyOf": true, "propertyOrdering": true,
	"minItems": true, "maxItems": true, "minLength": true, "maxLength": true, "pattern": true,
	"minimum": true, "maximum": true, "minProperties": true, "maxProperties": true,
}

// sanitizeSchema rewrites a JSON Schema (already $ref-inlined by gollama)
// into the subset the legacy parameters field accepts: keywords outside
// schemaKeys are dropped, oneOf becomes anyOf, const becomes a one-value
// enum, enum values become strings, a type list becomes its first non-null
// type plus nullable, and required lists only declared properties.
func sanitizeSchema(node any) any {
	n, ok := node.(map[string]any)
	if !ok {
		return node
	}
	out := make(map[string]any, len(n))
	for k, v := range n {
		switch k {
		case "const":
			if _, has := n["enum"]; !has {
				out["enum"] = stringEnum([]any{v})
			}
		case "oneOf":
			if _, has := n["anyOf"]; !has {
				out["anyOf"] = sanitizeList(v)
			}
		case "anyOf":
			out[k] = sanitizeList(v)
		case "items":
			out[k] = sanitizeSchema(v)
		case "format":
			if s, _ := v.(string); s == "enum" || s == "date-time" {
				out[k] = v
			}
		case "type":
			out[k] = v
			if list, ok := v.([]any); ok {
				t, nullable := "", false
				for _, x := range list {
					s, _ := x.(string)
					if s == "null" {
						nullable = true
					} else if t == "" {
						t = s
					}
				}
				if t == "" {
					t = "string"
				}
				out[k] = t
				if nullable {
					out["nullable"] = true
				}
			}
		case "properties":
			props, ok := v.(map[string]any)
			if !ok {
				continue
			}
			clean := make(map[string]any, len(props))
			for name, p := range props {
				clean[name] = sanitizeSchema(p)
			}
			out[k] = clean
		case "enum":
			out[k] = stringEnum(v)
		default:
			if schemaKeys[k] {
				out[k] = v
			}
		}
	}
	if req, ok := out["required"].([]any); ok {
		props, _ := out["properties"].(map[string]any)
		kept := req[:0:0]
		for _, r := range req {
			if name, ok := r.(string); ok && props[name] != nil {
				kept = append(kept, name)
			}
		}
		if len(kept) == 0 {
			delete(out, "required")
		} else {
			out["required"] = kept
		}
	}
	return out
}

func sanitizeList(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(list))
	for _, x := range list {
		out = append(out, sanitizeSchema(x))
	}
	return out
}

// stringEnum renders enum values as strings, the only form the legacy schema
// accepts.
func stringEnum(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, 0, len(list))
	for _, x := range list {
		switch s := x.(type) {
		case string:
			out = append(out, s)
		case nil:
		default:
			out = append(out, strings.Trim(fmt.Sprint(s), `"`))
		}
	}
	return out
}
