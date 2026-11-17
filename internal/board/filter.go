package board

import (
	"slices"
	"strings"
)

// Filter is GitHub's filter of a board's items (ProjectV2.items(query:)),
// the syntax of the board's filter bar: terms separated by spaces, all of
// which an item must match. Checked against GitHub (#94):
//
//   - A draft issue matches is:issue, is:draft and is:open; is:draft also
//     matches draft pull requests. Issues are FilterIssues, draft issues
//     FilterDrafts.
//   - A field is named by its name in lower case with hyphens for spaces
//     (languages-sprint:); a quoted name matches nothing.
//   - Values are quoted, case-insensitive, and a comma between them is OR:
//     status:"Backlog","In progress". Unquoted, "status:In progress" is
//     status:In and the text "progress".
//   - status:"Backlog",no:status is not an OR: it matches what
//     status:"Backlog" does. An OR with "no value" is the negation of the
//     other values: -status:"In progress","Done".
//   - Repeated terms are AND: label:"bug" label:"docs".
type Filter []string

// Terms that name the kinds of items.
const (
	FilterIssues = "is:issue -is:draft"
	FilterDrafts = "is:issue is:draft"
	FilterPulls  = "is:pr"
	FilterOpen   = "is:open"
	FilterMine   = "assignee:@me"
)

// String is the filter as GitHub reads it.
func (f Filter) String() string { return strings.Join(f, " ") }

// And is f with more terms, a copy.
func (f Filter) And(terms ...string) Filter {
	return append(slices.Clip(f), terms...)
}

// FieldTerm matches the items whose field named field has one of values;
// negate matches the others, items without a value included. ok is false
// when the filter cannot name the field or a value (see qualifier and
// quote): read the items unfiltered then.
func FieldTerm(field string, values []string, negate bool) (term string, ok bool) {
	q, ok := qualifier(field)
	if !ok || len(values) == 0 {
		return "", false
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		if quoted[i], ok = quote(v); !ok {
			return "", false
		}
	}
	term = q + ":" + strings.Join(quoted, ",")
	if negate {
		term = "-" + term
	}
	return term, true
}

// NoValue matches the items without a value in the field named field.
func NoValue(field string) (term string, ok bool) {
	q, ok := qualifier(field)
	if !ok {
		return "", false
	}
	return "no:" + q, true
}

// LabelTerm matches the items with label.
func LabelTerm(label string) (term string, ok bool) {
	v, ok := quote(label)
	if !ok {
		return "", false
	}
	return "label:" + v, true
}

// builtinQualifiers are the filter's own qualifiers: a field named like
// one cannot be told apart from it.
var builtinQualifiers = []string{
	"assignee", "assignees", "author", "closed", "created", "has", "is", "label", "labels",
	"linked-pull-requests", "milestone", "no", "parent-issue", "reason", "repo", "repository", "reviewers",
	"sub-issues-progress", "title", "type", "updated",
}

// qualifier is a custom field's name in a filter: lower case, a hyphen
// for each space. Only ASCII letters, digits, hyphens, underscores and
// spaces were checked against GitHub; any other name, or one that is a
// built-in qualifier, is not ok.
func qualifier(field string) (string, bool) {
	q := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(field), " ", "-"))
	if q == "" || slices.Contains(builtinQualifiers, q) {
		return "", false
	}
	for _, r := range q {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", false
		}
	}
	return q, true
}

// quote is a value in double quotes. A value with a double quote,
// backslash or comma has no checked spelling: not ok.
func quote(v string) (string, bool) {
	if v == "" || strings.ContainsAny(v, "\"\\,") {
		return "", false
	}
	return `"` + v + `"`, true
}
