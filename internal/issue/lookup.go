package issue

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Lookup names what a verb's effects need besides the card: label IDs by
// name, a user who can be assigned, the authenticated user. Read fetches
// them in the card's own request (principle 6), so a verb with labels and
// assign still sends one read and one mutation.
type Lookup struct {
	// Labels are label names, each looked up with repository.label(name:),
	// which matches the name exactly, case included.
	Labels []string
	// User is a login to find among the users who can be assigned issues
	// in the repository (assignableUsers), matched without case.
	User string
	// Viewer reads the authenticated user, for assign = "@me".
	Viewer bool
}

// Empty reports whether there is nothing to look up.
func (l Lookup) Empty() bool { return len(l.Labels) == 0 && l.User == "" && !l.Viewer }

// Found is what a Lookup found. Its IDs are for mutations only; never
// print them.
type Found struct {
	// Labels maps each name of Lookup.Labels that exists in the
	// repository to its label node ID; a missing name has no entry.
	Labels map[string]string
	// User is Lookup.User when it can be assigned in the repository, with
	// its login as GitHub spells it; nil otherwise.
	User *User
	// Viewer is the authenticated user when Lookup.Viewer is set.
	Viewer *User
}

// User is a GitHub user.
type User struct {
	ID    string `json:"id"` // node ID; never print it
	Login string `json:"login"`
}

// assignableWindow is how many assignable users matching Lookup.User are
// read; the login is then matched exactly among them.
const assignableWindow = 100

// The parts of cardQuery a Lookup extends.
const (
	cardHead = `query IssueCard($owner: String!, $repo: String!, $number: Int!, $comments: Int!, $status: String!`
	cardRepo = `) {
  repository(owner: $owner, name: $repo) {
    issueOrPullRequest(number: $number) ` + cardSelection
)

// labelAlias is the alias of the i-th label lookup and the name of its
// variable.
func labelAlias(i int) string { return fmt.Sprintf("label%d", i) }

// lookupQuery is cardQuery with the fields of l: aliased label lookups and
// the assignable user below the repository, the viewer at the top. It
// returns the extra variables too. An empty l gives cardQuery itself.
//
//	query IssueCard(..., $label0: String!, $user: String!) {
//	  repository(owner: $owner, name: $repo) {
//	    issueOrPullRequest(number: $number) { ... }
//	    label0: label(name: $label0) { id name }
//	    assignable: assignableUsers(query: $user, first: 100) { nodes { id login } }
//	  }
//	  viewer { id login }
//	}
func lookupQuery(l Lookup) (string, map[string]any) {
	if l.Empty() {
		return cardQuery, nil
	}
	vars := map[string]any{}
	var decls, repo strings.Builder
	for i, name := range l.Labels {
		alias := labelAlias(i)
		vars[alias] = name
		fmt.Fprintf(&decls, ", $%s: String!", alias)
		fmt.Fprintf(&repo, "\n    %s: label(name: $%s) { id name }", alias, alias)
	}
	if l.User != "" {
		vars["user"] = l.User
		decls.WriteString(", $user: String!")
		fmt.Fprintf(&repo, "\n    assignable: assignableUsers(query: $user, first: %d) { nodes { id login } }", assignableWindow)
	}
	top := ""
	if l.Viewer {
		top = "  viewer { id login }\n"
	}
	return cardHead + decls.String() + cardRepo + repo.String() + "\n  }\n" + top + "}\n\n" + cardFragments, vars
}

// repoJSON is the repository of a card response: the issue, and the
// lookups a Lookup added under their aliases.
type repoJSON struct {
	IssueOrPullRequest *issueJSON
	Assignable         *conn[*User]
	labels             map[string]*labelJSON // by alias
}

type labelJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (r *repoJSON) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key, raw := range fields {
		var err error
		switch {
		case key == "issueOrPullRequest":
			err = json.Unmarshal(raw, &r.IssueOrPullRequest)
		case key == "assignable":
			err = json.Unmarshal(raw, &r.Assignable)
		case strings.HasPrefix(key, "label"):
			if r.labels == nil {
				r.labels = map[string]*labelJSON{}
			}
			var l labelJSON
			if err = json.Unmarshal(raw, &l); err == nil && l.ID != "" {
				r.labels[key] = &l
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// found turns the lookups of a response into Found.
func (l Lookup) found(r *repoJSON, viewer *User) *Found {
	f := &Found{Labels: map[string]string{}}
	if r != nil {
		for i, name := range l.Labels {
			if lb := r.labels[labelAlias(i)]; lb != nil {
				f.Labels[name] = lb.ID
			}
		}
		if l.User != "" && r.Assignable != nil {
			for _, u := range r.Assignable.Nodes {
				if u != nil && u.ID != "" && strings.EqualFold(u.Login, l.User) {
					f.User = u
					break
				}
			}
		}
	}
	if l.Viewer && viewer != nil && viewer.ID != "" {
		f.Viewer = viewer
	}
	return f
}
