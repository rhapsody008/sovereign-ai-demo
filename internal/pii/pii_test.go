package pii

import "testing"

func TestMask(t *testing.T) {
	tests := []struct {
		e    Entity
		want string
	}{
		{Entity{Type: TypeName, Value: "Zhou Yi"}, "Z*** Y*"},
		{Entity{Type: TypeEmail, Value: "yi.zhou@nutanix.com"}, "y***@nutanix.com"},
		{Entity{Type: TypeNationalID, Value: "S1234567D"}, "S****567D"},
		{Entity{Type: TypePhone, Value: "+65 9123 4512"}, "+65 9*** **12"},
		{Entity{Type: TypePhone, Value: "91234512"}, "9*** **12"},
		{Entity{Type: TypeAddress, Value: "1 Raffles Place"}, "[ADDRESS]"},
		{Entity{Type: TypeDOB, Value: "1 Jan 1990"}, "[DOB]"},
		{Entity{Type: TypeOther, Value: "x"}, "[REDACTED]"},
	}
	for _, tt := range tests {
		if got := Mask(tt.e); got != tt.want {
			t.Errorf("Mask(%s) = %q, want %q", tt.e.Type, got, tt.want)
		}
	}
}

func TestSampleInput(t *testing.T) {
	in := "This is Zhou Yi with email yi.zhou@nutanix.com. File this."
	llm := []Entity{{Type: "PERSON_NAME", Value: "Zhou Yi"}}
	es := Merge(llm, Regex(in))
	if got, want := MaskText(in, es), "This is Z*** Y* with email y***@nutanix.com. File this."; got != want {
		t.Fatalf("masked = %q, want %q", got, want)
	}
	if c := Classify("Public", es); c != "Confidential" {
		t.Fatalf("classification = %q", c)
	}
	if es[1].Source != "regex" {
		t.Fatalf("email should come from regex backstop: %+v", es)
	}
}

func TestHeuristicNames(t *testing.T) {
	es := HeuristicNames("This is Zhou Yi with email yi.zhou@nutanix.com.")
	if len(es) != 1 || es[0].Value != "Zhou Yi" {
		t.Fatalf("got %+v", es)
	}
}

func TestParseLLM(t *testing.T) {
	for _, in := range []string{
		`{"entities":[{"type":"EMAIL","value":"a@b.co"}],"summary":"s {x}","classification":"Confidential"}`,
		"```json\n{\"entities\":[{\"type\":\"EMAIL\",\"value\":\"a@b.co\"}],\"summary\":\"s {x}\"}\n```",
		"Sure! Here you go: {\"entities\":[{\"type\":\"EMAIL\",\"value\":\"a@b.co\"}],\"summary\":\"s {x}\"} hope it helps",
	} {
		r, err := ParseLLM(in)
		if err != nil || len(r.Entities) != 1 || r.Summary != "s {x}" {
			t.Errorf("ParseLLM(%q) = %+v, %v", in, r, err)
		}
	}
	if _, err := ParseLLM("I cannot help {with that"); err == nil {
		t.Error("expected error for garbled input")
	}
}

func TestRestricted(t *testing.T) {
	es := Merge(nil, Regex("NRIC S1234567D"))
	if c := Classify("", es); c != "Restricted" {
		t.Fatalf("got %q", c)
	}
}
