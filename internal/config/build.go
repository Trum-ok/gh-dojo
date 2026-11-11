package config

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	verbNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	repoRE     = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
)

// build validates the merged raw config and fills c. Checks run in file
// order, and the first problem is returned.
func (c *Config) build(r *rawFile) error {
	c.Version = Version
	if r.Version != nil && *r.Version != Version {
		return c.Errorf("version %d is not supported", *r.Version).WithAllowed(strconv.Itoa(Version))
	}
	if r.Preset != nil {
		c.Preset = *r.Preset
	}
	if err := c.buildBoard(r.Board); err != nil {
		return err
	}
	if err := c.buildFields(r.Fields); err != nil {
		return err
	}
	if err := c.buildStatuses(r.Statuses); err != nil {
		return err
	}
	c.Labels = Labels{TypePrefix: deref(r.Labels.TypePrefix), AreaPrefix: deref(r.Labels.AreaPrefix)}
	if err := c.buildSession(r.Session); err != nil {
		return err
	}
	for _, name := range r.verbOrder {
		rv := r.Verbs[name]
		if rv.Disabled != nil && *rv.Disabled {
			continue
		}
		v, err := c.buildVerb(name, rv)
		if err != nil {
			return err
		}
		c.Verbs = append(c.Verbs, v)
	}
	return nil
}

func (c *Config) buildBoard(r rawBoard) error {
	b := Board{Owner: strings.TrimSpace(deref(r.Owner)), Assignee: deref(r.Assignee)}
	if b.Owner == "" {
		return c.Errorf("board.owner is required: the user or org login that owns the project")
	}
	if r.Project == nil {
		return c.Errorf("board.project is required: the project number from its URL")
	}
	if b.Project = *r.Project; b.Project < 1 {
		return c.Errorf("board.project must be a project number, 1 or greater")
	}
	if r.Repos != nil {
		for _, repo := range *r.Repos {
			if !repoRE.MatchString(repo) {
				return c.Errorf("board.repos: %q is not owner/name", repo)
			}
		}
		b.Repos = *r.Repos
	}
	c.Board = b
	return nil
}

func (c *Config) buildFields(fields map[string]string) error {
	if _, ok := fields["status"]; !ok {
		return c.Errorf("fields.status is required: the name of the board's status field")
	}
	c.Fields = make(map[string]string, len(fields))
	for k, v := range fields {
		c.Fields[k] = v
	}
	for _, k := range c.FieldNames() {
		if strings.TrimSpace(c.Fields[k]) == "" {
			return c.Errorf("%s is empty: set the name of the board field", c.KeyPath("fields."+k))
		}
	}
	return nil
}

func (c *Config) buildStatuses(r rawStatuses) error {
	c.Statuses.Initial = deref(r.Initial)
	if r.Final != nil {
		if slices.Contains(*r.Final, "") {
			return c.Errorf("%s has an empty status", c.KeyPath("statuses.final"))
		}
		c.Statuses.Final = *r.Final
	}
	return nil
}

func (c *Config) buildSession(r rawSession) error {
	if r.Env != nil {
		if slices.Contains(*r.Env, "") {
			return c.Errorf("%s has an empty variable name", c.KeyPath("session.env"))
		}
		c.Session.Env = *r.Env
	}
	c.Session.Link = "{id}"
	if r.Link != nil {
		key := c.KeyPath("session.link")
		if bad, ok := checkTemplate(*r.Link, linkVars); !ok {
			return c.Errorf("%s: unknown placeholder {%s}", key, bad).WithAllowed("{id}")
		}
		if !strings.Contains(*r.Link, "{id}") {
			return c.Errorf("%s must contain {id}, the session ID", key)
		}
		c.Session.Link = *r.Link
	}
	return nil
}

func (c *Config) buildVerb(name string, r *rawVerb) (Verb, error) {
	p := "verbs." + name + "."
	if !verbNameRE.MatchString(name) {
		return Verb{}, c.Errorf("verb name %q is not a valid command name: use lowercase letters, digits and dashes", name)
	}
	if slices.Contains(Builtins, name) {
		return Verb{}, c.Errorf("verb name %q is taken by a built-in command; rename [verbs.%s]", name, name)
	}
	v := Verb{Name: name, When: strings.TrimSpace(deref(r.When)), To: deref(r.To), Comment: deref(r.Comment)}
	switch {
	case v.When == "":
		return Verb{}, c.Errorf("%s is required: one line saying when to use the verb", c.KeyPath(p+"when"))
	case strings.Contains(v.When, "\n"):
		return Verb{}, c.Errorf("%s must be one line", c.KeyPath(p+"when"))
	}
	if r.From != nil {
		if slices.Contains(*r.From, "") {
			return Verb{}, c.Errorf("%s has an empty status", c.KeyPath(p+"from"))
		}
		v.From = *r.From
	}
	for _, list := range []struct {
		key  string
		src  *[]string
		dest *[]string
	}{{"require", r.Require, &v.Require}, {"require_one_of", r.RequireOneOf, &v.RequireOneOf}} {
		if list.src == nil {
			continue
		}
		for _, arg := range *list.src {
			if !slices.Contains(Args, arg) {
				return Verb{}, c.Errorf("%s: unknown argument %q", c.KeyPath(p+list.key), arg).WithAllowed(Args...)
			}
		}
		*list.dest = *list.src
	}
	if bad, ok := checkTemplate(v.Comment, commentVars); !ok {
		allowed := make([]string, len(commentVars))
		for i, name := range commentVars {
			allowed[i] = "{" + name + "}"
		}
		return Verb{}, c.Errorf("%s: unknown placeholder {%s}", c.KeyPath(p+"comment"), bad).WithAllowed(allowed...)
	}
	return v, nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
