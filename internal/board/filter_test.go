package board

import "testing"

// TestFilterTerms: terms spelled as GitHub reads them (checked against
// GitHub, #94), and the names and values with no checked spelling refused.
func TestFilterTerms(t *testing.T) {
	for _, tt := range []struct {
		name string
		term func() (string, bool)
		want string // "" for not ok
	}{
		{"one value", func() (string, bool) { return FieldTerm("Status", []string{"In progress"}, false) }, `status:"In progress"`},
		{"or", func() (string, bool) { return FieldTerm("Status", []string{"Backlog", "🔖 Todo"}, false) }, `status:"Backlog","🔖 Todo"`},
		{"negated", func() (string, bool) { return FieldTerm("Status", []string{"Ready", "Done"}, true) }, `-status:"Ready","Done"`},
		{"field with spaces", func() (string, bool) { return FieldTerm("Languages Sprint", []string{"S1"}, false) }, `languages-sprint:"S1"`},
		{"default iteration field", func() (string, bool) { return FieldTerm("Iteration", []string{"Iteration 2"}, false) }, `iteration:"Iteration 2"`},
		{"no value", func() (string, bool) { return NoValue("Sprint") }, "no:sprint"},
		{"label", func() (string, bool) { return LabelTerm("type: bug") }, `label:"type: bug"`},
		{"no values", func() (string, bool) { return FieldTerm("Status", nil, false) }, ""},
		{"quote in a value", func() (string, bool) { return FieldTerm("Status", []string{`Say "hi"`}, false) }, ""},
		{"comma in a label", func() (string, bool) { return LabelTerm("wont,fix") }, ""},
		{"empty label", func() (string, bool) { return LabelTerm("") }, ""},
		{"built-in qualifier", func() (string, bool) { return FieldTerm("Label", []string{"x"}, false) }, ""},
		{"not ASCII", func() (string, bool) { return FieldTerm("Статус", []string{"x"}, false) }, ""},
		{"punctuation", func() (string, bool) { return NoValue("Sprint (2w)") }, ""},
	} {
		got, ok := tt.term()
		if ok != (tt.want != "") || got != tt.want {
			t.Errorf("%s: got %q, %v; want %q", tt.name, got, ok, tt.want)
		}
	}
}

func TestFilterAnd(t *testing.T) {
	base := Filter{FilterIssues}
	a := base.And(FilterOpen)
	b := base.And(FilterMine)
	if a.String() != "is:issue -is:draft is:open" || b.String() != "is:issue -is:draft assignee:@me" {
		t.Errorf("And shares its slice: %q, %q", a, b)
	}
	if Filter(nil).String() != "" {
		t.Errorf("empty filter: %q", Filter(nil).String())
	}
}
