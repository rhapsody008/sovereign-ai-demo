// Package pii detects and masks personal data and parses LLM scan output.
package pii

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	TypeName       = "PERSON_NAME"
	TypeEmail      = "EMAIL"
	TypePhone      = "PHONE"
	TypeNationalID = "NATIONAL_ID"
	TypeAddress    = "ADDRESS"
	TypeDOB        = "DOB"
	TypeOther      = "OTHER"
)

// Entity holds a raw value in memory only; it must never be persisted.
type Entity struct {
	Type   string `json:"type"`
	Value  string `json:"value"`
	Source string `json:"source,omitempty"`
}

// LLMResult is the JSON object the system prompt asks the model to return.
type LLMResult struct {
	Entities        []Entity `json:"entities"`
	TranscribedText string   `json:"transcribedText"`
	Summary         string   `json:"summary"`
	Classification  string   `json:"classification"`
}

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reNRIC  = regexp.MustCompile(`\b[STFGM]\d{7}[A-Z]\b`)
	rePhone = regexp.MustCompile(`(\+65\s?)?\b[689]\d{3}\s?\d{4}\b`)
	// Naive "This is Zhou Yi" / "My name is Jane Tan" heuristic, used only when
	// the model output is unusable (and by the mock NAI server).
	reName = regexp.MustCompile(`(?:(?i:this is|visitor|contact|i am|i'm|my name is|name:|name is|mr\.?|ms\.?|mrs\.?|dr\.?)\s+)((?:[A-Z][a-z]+)(?:\s+[A-Z][a-z]+){0,2})`)
)

// Regex runs the deterministic backstop detectors.
func Regex(text string) []Entity {
	var out []Entity
	for _, m := range reEmail.FindAllString(text, -1) {
		out = append(out, Entity{Type: TypeEmail, Value: m, Source: "regex"})
	}
	for _, m := range reNRIC.FindAllString(text, -1) {
		out = append(out, Entity{Type: TypeNationalID, Value: m, Source: "regex"})
	}
	for _, m := range rePhone.FindAllString(text, -1) {
		out = append(out, Entity{Type: TypePhone, Value: strings.TrimSpace(m), Source: "regex"})
	}
	return out
}

// HeuristicNames finds capitalised names after common introductions.
func HeuristicNames(text string) []Entity {
	var out []Entity
	for _, m := range reName.FindAllStringSubmatch(text, -1) {
		out = append(out, Entity{Type: TypeName, Value: m[1], Source: "regex"})
	}
	return out
}

// Merge combines LLM and regex entities, deduplicating by value.
func Merge(llm, regex []Entity) []Entity {
	seen := map[string]bool{}
	var out []Entity
	add := func(e Entity, src string) {
		e.Value = strings.TrimSpace(e.Value)
		k := strings.ToLower(e.Value)
		if e.Value == "" || seen[k] {
			return
		}
		seen[k] = true
		e.Type = normType(e.Type)
		if e.Source == "" {
			e.Source = src
		}
		out = append(out, e)
	}
	for _, e := range llm {
		e.Source = "llm"
		add(e, "llm")
	}
	for _, e := range regex {
		add(e, "regex")
	}
	return out
}

func normType(t string) string {
	switch t = strings.ToUpper(strings.TrimSpace(t)); t {
	case TypeName, TypeEmail, TypePhone, TypeNationalID, TypeAddress, TypeDOB:
		return t
	case "NAME", "PERSON":
		return TypeName
	case "NRIC", "FIN", "ID":
		return TypeNationalID
	}
	return TypeOther
}

var classRank = map[string]int{"Public": 0, "Internal": 1, "Confidential": 2, "Restricted": 3}

// Classify applies the floor: Confidential if any entity, Restricted if a national ID.
func Classify(llm string, entities []Entity) string {
	c := "Internal"
	for k := range classRank {
		if strings.EqualFold(k, strings.TrimSpace(llm)) {
			c = k
		}
	}
	floor := "Public"
	for _, e := range entities {
		if e.Type == TypeNationalID {
			floor = "Restricted"
			break
		}
		floor = "Confidential"
	}
	if classRank[floor] > classRank[c] {
		return floor
	}
	return c
}

// Mask returns the masked form of a single entity value.
func Mask(e Entity) string {
	v := e.Value
	switch e.Type {
	case TypeName:
		words := strings.Fields(v)
		for i, w := range words {
			r := []rune(w)
			stars := "*"
			if i == 0 {
				stars = "***"
			}
			words[i] = string(r[0]) + stars
		}
		return strings.Join(words, " ")
	case TypeEmail:
		at := strings.LastIndex(v, "@")
		if at <= 0 {
			return "[EMAIL]"
		}
		return string([]rune(v)[0]) + "***" + v[at:]
	case TypeNationalID:
		if len(v) < 6 {
			return "[ID]"
		}
		return v[:1] + "****" + v[len(v)-4:]
	case TypePhone:
		return maskPhone(v)
	case TypeAddress:
		return "[ADDRESS]"
	case TypeDOB:
		return "[DOB]"
	}
	return "[REDACTED]"
}

func maskPhone(v string) string {
	var digits []rune
	for _, r := range v {
		if unicode.IsDigit(r) {
			digits = append(digits, r)
		}
	}
	prefix := ""
	if strings.HasPrefix(strings.TrimSpace(v), "+") && len(digits) > 8 {
		cc := len(digits) - 8
		prefix = "+" + string(digits[:cc]) + " "
		digits = digits[cc:]
	}
	if len(digits) < 4 {
		return "[PHONE]"
	}
	n := len(digits)
	last := string(digits[n-2:])
	return prefix + string(digits[0]) + strings.Repeat("*", 3) + " " + strings.Repeat("*", max(n-6, 0)) + last
}

// MaskText replaces every entity value in text, longest first.
func MaskText(text string, entities []Entity) string {
	es := append([]Entity(nil), entities...)
	sort.SliceStable(es, func(i, j int) bool { return len(es[i].Value) > len(es[j].Value) })
	for _, e := range es {
		if e.Value == "" {
			continue
		}
		m := Mask(e)
		if strings.Contains(text, e.Value) {
			text = strings.ReplaceAll(text, e.Value, m)
			continue
		}
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(e.Value))
		text = re.ReplaceAllLiteralString(text, m)
	}
	return text
}

// ParseLLM extracts the first balanced JSON object from a model response,
// tolerating ```json fences and surrounding prose.
func ParseLLM(content string) (*LLMResult, error) {
	s := strings.TrimSpace(content)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	obj := firstObject(s)
	if obj == "" {
		return nil, errors.New("no JSON object in response")
	}
	var r LLMResult
	if err := json.Unmarshal([]byte(obj), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func firstObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
