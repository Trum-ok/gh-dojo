package config

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	verbNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	repoRE     = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	loginRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
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
	if err := c.buildLabels(r.Labels); err != nil {
		return err
	}
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
	b := Board{Owner: strings.TrimSpace(deref(r.Owner))}
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
		if err := c.checkList("board.repos", *r.Repos); err != nil {
			return err
		}
		for _, repo := range *r.Repos {
			if !repoRE.MatchString(repo) || strings.HasSuffix(repo, ".git") {
				return c.Errorf("board.repos: %q is not owner/name", repo)
			}
		}
		b.Repos = *r.Repos
	}
	if r.Assignee != nil {
		if err := c.checkAssignee("board.assignee", *r.Assignee); err != nil {
			return err
		}
		b.Assignee = *r.Assignee
	}
	c.Board = b
	return nil
}

// checkAssignee accepts Me or a GitHub login.
func (c *Config) checkAssignee(key, s string) error {
	switch {
	case s == Me:
		return nil
	case strings.HasPrefix(s, "@"):
		return c.Errorf("%s: %q: write the login without @, or %q for the authenticated user", c.KeyPath(key), s, Me)
	case !loginRE.MatchString(s):
		return c.Errorf("%s: %q is not a GitHub login; use a login or %q for the authenticated user", c.KeyPath(key), s, Me)
	}
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
		if err := c.checkName("fields."+k, c.Fields[k]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) buildStatuses(r rawStatuses) error {
	if r.Initial != nil && *r.Initial != "" {
		if err := c.checkName("statuses.initial", *r.Initial); err != nil {
			return err
		}
		c.Statuses.Initial = *r.Initial
	}
	if r.Final != nil {
		if err := c.checkList("statuses.final", *r.Final); err != nil {
			return err
		}
		c.Statuses.Final = *r.Final
	}
	if r.Resolved != nil {
		if err := c.checkList("statuses.resolved", *r.Resolved); err != nil {
			return err
		}
		c.Statuses.Resolved = *r.Resolved
	}
	return nil
}

func (c *Config) buildLabels(r rawLabels) error {
	c.Labels.AreaPrefix = deref(r.AreaPrefix)
	if r.Types == nil {
		return nil
	}
	if err := c.checkList("labels.types", *r.Types); err != nil {
		return err
	}
	for _, t := range *r.Types {
		if p := c.Labels.AreaPrefix; p != "" && strings.HasPrefix(t, p) {
			return c.Errorf("%s: %q starts with labels.area_prefix %q", c.KeyPath("labels.types"), t, p)
		}
	}
	c.Labels.Types = *r.Types
	return nil
}

func (c *Config) buildSession(r rawSession) error {
	if r.Env != nil {
		if err := c.checkList("session.env", *r.Env); err != nil {
			return err
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
	if err := c.checkVerbName(name); err != nil {
		return Verb{}, err
	}
	v := Verb{Name: name, When: strings.TrimSpace(deref(r.When)), To: deref(r.To), Comment: deref(r.Comment)}
	if r.Comment == nil && requires(r, ArgComment) {
		// A verb that requires --comment without a template posts the text
		// as is: [verbs.drop] with require = ["comment"] needs nothing more.
		v.Comment = "{" + VarComment + "}"
	}
	switch {
	case v.When == "":
		return Verb{}, c.Errorf("%s is required: one line saying when to use the verb", c.KeyPath(p+"when"))
	case strings.ContainsAny(v.When, "\r\n"):
		return Verb{}, c.Errorf("%s must be one line", c.KeyPath(p+"when"))
	}
	if v.To != "" {
		if err := c.checkName(p+"to", v.To); err != nil {
			return Verb{}, err
		}
	}
	if r.From != nil {
		if err := c.checkList(p+"from", *r.From); err != nil {
			return Verb{}, err
		}
		v.From = *r.From
	}
	if bad, ok := checkTemplate(v.Comment, commentVars); !ok {
		allowed := make([]string, len(commentVars))
		for i, name := range commentVars {
			allowed[i] = "{" + name + "}"
		}
		return Verb{}, c.Errorf("%s: unknown placeholder {%s}", c.KeyPath(p+"comment"), bad).WithAllowed(allowed...)
	}
	if err := c.buildEffects(name, r, &v); err != nil {
		return Verb{}, err
	}
	if v.To == "" && v.Comment == "" && len(v.AddLabels)+len(v.RemoveLabels) == 0 && v.Assign == "" &&
		v.Close == "" && !v.Reopen {
		return Verb{}, c.Errorf("verbs.%s has no effect: set to, comment, labels, assign, close or reopen", name)
	}
	for _, list := range []struct {
		key  string
		src  *[]string
		dest *[]string
	}{{"require", r.Require, &v.Require}, {"require_one_of", r.RequireOneOf, &v.RequireOneOf}} {
		if list.src == nil {
			continue
		}
		key := c.KeyPath(p + list.key)
		if err := c.checkList(p+list.key, *list.src); err != nil {
			return Verb{}, err
		}
		for _, arg := range *list.src {
			if !slices.Contains(args, arg) {
				return Verb{}, c.Errorf("%s: unknown argument %q", key, arg).WithAllowed(args...)
			}
			if list.key == "require_one_of" && slices.Contains(v.Require, arg) {
				return Verb{}, c.Errorf("%s: %q is also in %s", key, arg, c.KeyPath(p+"require"))
			}
			if !v.Uses(arg) {
				return Verb{}, c.Errorf("%s names %q, but %s does not use {%s}", key, arg, c.KeyPath(p+"comment"), arg)
			}
		}
		*list.dest = *list.src
	}
	return v, nil
}

// requires reports whether the verb names arg in require or require_one_of.
func requires(r *rawVerb, arg string) bool {
	for _, list := range []*[]string{r.Require, r.RequireOneOf} {
		if list != nil && slices.Contains(*list, arg) {
			return true
		}
	}
	return false
}

// buildEffects validates labels, assign, close, reopen and confirm.
func (c *Config) buildEffects(name string, r *rawVerb, v *Verb) error {
	p := "verbs." + name + "."
	if r.Labels != nil {
		key := c.KeyPath(p + "labels")
		if err := c.checkList(p+"labels", *r.Labels); err != nil {
			return err
		}
		seen := map[string]string{} // folded name → entry
		for _, entry := range *r.Labels {
			label := entry[1:]
			switch {
			case entry[0] != '+' && entry[0] != '-':
				return c.Errorf("%s: %q must start with + to add the label or - to remove it", key, entry).
					WithFix(fmt.Sprintf(`labels = ["+%s"] adds it, labels = ["-%s"] removes it`, entry, entry))
			case nameProblem(label) != "":
				return c.Errorf("%s: %q: the label name %s", key, entry, nameProblem(label))
			}
			if prev, ok := seen[strings.ToLower(label)]; ok {
				return c.Errorf("%s lists %q and %q; keep one", key, prev, entry)
			}
			seen[strings.ToLower(label)] = entry
			if entry[0] == '+' {
				v.AddLabels = append(v.AddLabels, label)
			} else {
				v.RemoveLabels = append(v.RemoveLabels, label)
			}
		}
	}
	if a := r.Assign; a != nil {
		key := c.KeyPath(p + "assign")
		switch {
		case a.Bool != nil && *a.Bool && c.Board.Assignee == "":
			return c.Errorf("%s = true assigns board.assignee, which is not set", key).
				WithFix(fmt.Sprintf(`add assignee = %q under [board] in %s, or set assign = %q`, Me, c.Display, Me))
		case a.Bool != nil && *a.Bool:
			v.Assign = c.Board.Assignee
		case a.User != nil:
			if err := c.checkAssignee(p+"assign", *a.User); err != nil {
				return err
			}
			v.Assign = *a.User
		}
	}
	if r.Close != nil {
		if !slices.Contains(closeReasons, *r.Close) {
			return c.Errorf("%s: unknown reason %q", c.KeyPath(p+"close"), *r.Close).WithAllowed(closeReasons...)
		}
		v.Close = *r.Close
	}
	v.Reopen = deref(r.Reopen)
	if v.Close != "" && v.Reopen {
		return c.Errorf("verbs.%s sets both close and reopen; keep one", name)
	}
	if r.Confirm != nil {
		if *r.Confirm != ConfirmUser {
			return c.Errorf("%s: unknown value %q", c.KeyPath(p+"confirm"), *r.Confirm).WithAllowed(ConfirmUser)
		}
		v.Confirm = true
	}
	return nil
}

// checkRepoVerbs checks the verb names of the repo config, including disabled
// ones, and that disabled = true stands alone and removes a preset verb.
func (c *Config) checkRepoVerbs(repo, preset *rawFile) error {
	for _, name := range repo.verbOrder {
		if err := c.checkVerbName(name); err != nil {
			return err
		}
		v := repo.Verbs[name]
		if v.Disabled == nil || !*v.Disabled {
			continue
		}
		if other := v.setKeys(); len(other) > 0 {
			return c.Errorf("verbs.%s.%s is set next to disabled = true; remove one of them", name, other[0])
		}
		if preset == nil {
			return c.Errorf("verbs.%s: disabled = true removes a preset verb, but no preset is set", name)
		}
		if preset.Verbs[name] == nil {
			return c.Errorf("verbs.%s: disabled = true, but preset %s has no verb %q", name, *repo.Preset, name).
				WithAllowed(preset.verbOrder...)
		}
	}
	return nil
}

// checkVerbName rejects names that are not valid commands or shadow a
// built-in command.
func (c *Config) checkVerbName(name string) error {
	if !verbNameRE.MatchString(name) {
		return c.Errorf("verb name %q is not a valid command name: use lowercase letters, digits and dashes", name)
	}
	if slices.Contains(builtins, name) {
		return c.Errorf("verb name %q is taken by a built-in command; rename [verbs.%s]", name, name)
	}
	return nil
}

// checkName rejects an empty name and one with surrounding whitespace.
func (c *Config) checkName(key, s string) error {
	if problem := nameProblem(s); problem != "" {
		return c.Errorf("%s %s", c.KeyPath(key), problem)
	}
	return nil
}

// checkList rejects empty entries, entries with surrounding whitespace and
// duplicates.
func (c *Config) checkList(key string, list []string) error {
	for i, s := range list {
		if problem := nameProblem(s); problem != "" {
			return c.Errorf("%s: entry %d %s", c.KeyPath(key), i+1, problem)
		}
		if slices.Index(list, s) < i {
			return c.Errorf("%s lists %q twice", c.KeyPath(key), s)
		}
	}
	return nil
}

func nameProblem(s string) string {
	switch {
	case strings.TrimSpace(s) == "":
		return "is empty"
	case strings.TrimSpace(s) != s:
		return fmt.Sprintf("%q has leading or trailing spaces", s)
	}
	return ""
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
