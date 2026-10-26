package audit

import (
	"strconv"
	"strings"
)

// Facts is what one evidence record says about a resource, normalised across the many
// shapes the evidence takes (SQL rows, JSON exports, log events, vision extractions).
// Each field is tri-state: +1 yes, -1 no, 0 not stated.
type Facts struct {
	Exists  int // the resource/record exists
	Ident   int // direct identifiers (email, phone, name, hash) are exposed
	Ref     int // a stable user reference (user_id / customer_reference) is attached
	Enabled int // a data flow is enabled
	Restore int // a backup can be restored
}

var identKeys = map[string]bool{
	"email": true, "phone": true, "email_hash": true, "full_name": true, "name": true, "display_name": true,
	"customer_email": true, "owner_email": true, "owner_phone": true, "requester_email": true,
	"requester_name": true, "requester_phone": true, "owner": true,
}

var refKeys = map[string]bool{"user_id": true, "customer_reference": true}

// FactsOf derives Facts from a decoded record.
func FactsOf(raw map[string]any) Facts {
	var f Facts
	leaves := map[string][]any{}
	collect(raw, leaves)

	for k, vs := range leaves {
		for _, v := range vs {
			s := strings.ToLower(strings.TrimSpace(str(v)))
			null := v == nil || s == "" || s == "null" || s == "none"
			switch k {
			case "rows", "total_hits", "total":
				if n, err := strconv.Atoi(s); err == nil {
					f.Exists = merge(f.Exists, sign(n > 0))
				}
			case "status_code":
				switch s {
				case "200":
					f.Exists = merge(f.Exists, 1)
				case "404":
					f.Exists = merge(f.Exists, -1)
				}
			case "result":
				switch s {
				case "found", "hit", "identifiable", "active", "anonymized", "redacted", "disabled", "blocked":
					f.Exists = merge(f.Exists, 1)
				case "not_found", "miss":
					f.Exists = merge(f.Exists, -1)
				}
				switch s {
				case "identifiable":
					f.Ident = merge(f.Ident, 1)
				case "anonymized", "redacted":
					f.Ident = merge(f.Ident, -1)
				case "active":
					f.Enabled = merge(f.Enabled, 1)
					f.Restore = merge(f.Restore, 1)
				case "disabled":
					f.Enabled = merge(f.Enabled, -1)
				case "blocked":
					f.Restore = merge(f.Restore, -1)
				}
			case "available", "download_available", "archive_present":
				if b, ok := boolOf(s); ok {
					f.Exists = merge(f.Exists, sign(b))
				}
			case "marker_found":
				if b, ok := boolOf(s); ok && !b {
					f.Exists = merge(f.Exists, 1) // replica never saw the deletion marker
				}
			case "enabled":
				if b, ok := boolOf(s); ok {
					f.Enabled = merge(f.Enabled, sign(b))
				}
			case "restore_allowed":
				if b, ok := boolOf(s); ok {
					f.Restore = merge(f.Restore, sign(b))
				}
			case "decision":
				if s == "deny" {
					f.Restore = merge(f.Restore, -1)
				}
			case "eligible":
				if b, ok := boolOf(s); ok {
					f.Restore = merge(f.Restore, sign(b))
				}
			case "direct_identifiers":
				if n, err := strconv.Atoi(s); err == nil {
					f.Ident = merge(f.Ident, sign(n > 0))
				}
			case "matched_value":
				if !null {
					f.Ident = merge(f.Ident, 1)
				}
			}
			if identKeys[k] {
				f.Ident = merge(f.Ident, sign(!null))
			}
			if refKeys[k] {
				f.Ref = merge(f.Ref, sign(!null))
			}
		}
	}
	// A profile object that is explicitly null with a 404 is a negative lookup.
	if p, ok := raw["profile"]; ok && p == nil {
		f.Exists = merge(f.Exists, -1)
	}
	return f
}

// merge combines two tri-state values: any positive wins over negative, negative over unknown.
func merge(a, b int) int {
	if a == 1 || b == 1 {
		return 1
	}
	if a == -1 || b == -1 {
		return -1
	}
	return 0
}

func sign(b bool) int {
	if b {
		return 1
	}
	return -1
}

func boolOf(s string) (bool, bool) {
	switch s {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	}
	return false, false
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return strings.TrimSpace(strings.Trim(strings.ReplaceAll(strings.TrimSpace(toString(t)), "\n", " "), `"`))
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		if s, ok := v.(interface{ String() string }); ok {
			return s.String()
		}
		return ""
	}
}

func collect(v any, out map[string][]any) {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			lk := strings.ToLower(k)
			switch cc := c.(type) {
			case map[string]any, []any:
				collect(cc, out)
			default:
				out[lk] = append(out[lk], cc)
			}
		}
	case []any:
		for _, c := range t {
			collect(c, out)
		}
	}
}
