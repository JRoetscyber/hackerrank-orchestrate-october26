// Package evidence turns heterogeneous evidence files (SQL snapshots, JSON,
// logs, text, images, PDFs) into uniform records + flattened searchable fields.
package evidence

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SnapKey identifies a point-in-time slice inside one file.
type SnapKey struct {
	System     string
	Collection string
	Topology   string
	Table      string
	TS         string
}

// Record is one evidence entity (a DB row, a JSON object, a log line, a UI row...).
type Record struct {
	Snap       SnapKey
	System     string
	Resource   string
	Collection string
	RecordType string
	ObservedAt string
	Locator    string
	Trace      string
	Raw        map[string]any
}

// Field is one flattened leaf of a record.
type Field struct {
	Path string
	Key  string
	// Value is nil for JSON null.
	Value *string
	Norm  string
	Kind  string // email | email_hash | phone | user_id | resource | name | text | null
}

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,}`)
	reHash  = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)
	reUser  = regexp.MustCompile(`(?i)\buser_\d+\b`)
	rePhone = regexp.MustCompile(`^\+\d[\d\s().\-]{5,}\d$`)
	reRes   = regexp.MustCompile(`^[a-z][a-z_]*_\d+$`)
	reSpace = regexp.MustCompile(`\s+`)
	// resource-id-shaped tokens inside free text, e.g. "CS-4827 / support_001"
	reEmbRes = regexp.MustCompile(`(?i)\b[a-z][a-z_]*_\d+\b`)
)

var nameKeys = map[string]bool{
	"name": true, "full_name": true, "display_name": true, "customer_name": true,
	"contact_name": true, "account_name": true, "subject_name": true,
}

// Flatten emits every leaf of raw as a classified Field. Free-text strings are also
// scanned for embedded emails, hashes and user references so an identifier buried in a
// log message or support body is still searchable.
func Flatten(raw map[string]any) []Field {
	var out []Field
	flat("", raw, &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func flat(path string, v any, out *[]Field) {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			p := k
			if path != "" {
				p = path + "." + k
			}
			flat(p, c, out)
		}
	case []any:
		for i, c := range t {
			flat(fmt.Sprintf("%s[%d]", path, i), c, out)
		}
	case nil:
		*out = append(*out, Field{Path: path, Key: lastKey(path), Kind: "null"})
	default:
		s := scalarString(t)
		key := lastKey(path)
		kind, norm := Classify(key, s)
		sv := s
		*out = append(*out, Field{Path: path, Key: key, Value: &sv, Norm: norm, Kind: kind})
		if kind == "text" || kind == "resource" || kind == "name" {
			embedded(path, key, s, out)
		}
	}
}

func embedded(path, key, s string, out *[]Field) {
	add := func(suffix, kind, val, norm string) {
		v := val
		*out = append(*out, Field{Path: path + suffix, Key: key, Value: &v, Norm: norm, Kind: kind})
	}
	for _, m := range reEmail.FindAllString(s, -1) {
		add("~email", "email", m, strings.ToLower(m))
	}
	for _, m := range reHash.FindAllString(s, -1) {
		add("~hash", "email_hash", m, strings.ToLower(m))
	}
	for _, m := range reUser.FindAllString(s, -1) {
		add("~user", "user_id", m, strings.ToLower(m))
	}
	for _, m := range reEmbRes.FindAllString(s, -1) {
		if !reUser.MatchString(m) && m != s {
			add("~resource", "resource", m, strings.ToLower(m))
		}
	}
}

func lastKey(path string) string {
	k := path
	if i := strings.LastIndex(k, "."); i >= 0 {
		k = k[i+1:]
	}
	if i := strings.Index(k, "["); i >= 0 {
		k = k[:i]
	}
	return k
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}

// Classify guesses what kind of identifier a value is, using both key hints and shape.
func Classify(key, v string) (kind, norm string) {
	v = strings.TrimSpace(v)
	lk := strings.ToLower(key)
	switch {
	case v == "":
		return "text", ""
	case reEmail.FindString(v) == v:
		return "email", strings.ToLower(v)
	case len(v) == 64 && reHash.FindString(v) == v:
		return "email_hash", strings.ToLower(v)
	case reUser.FindString(v) == v:
		return "user_id", strings.ToLower(v)
	case strings.Contains(lk, "phone") || rePhone.MatchString(v):
		if d := NormPhone(v); len(d) >= 7 {
			return "phone", d
		}
	case nameKeys[lk]:
		return "name", NormName(v)
	case reRes.MatchString(strings.ToLower(v)):
		return "resource", strings.ToLower(v)
	}
	if len(v) <= 200 {
		return "text", strings.ToLower(v)
	}
	return "text", ""
}

// NormPhone keeps digits only so "+34 612 847 593" == "34612847593".
func NormPhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormName lowercases and collapses whitespace.
func NormName(s string) string {
	return reSpace.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), " ")
}

// NormTS converts any recognised timestamp to UTC RFC3339 ("...Z"); unknown input is returned unchanged.
func NormTS(s string) string {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05Z")
		}
	}
	return s
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
