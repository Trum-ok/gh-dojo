package config

import (
	"fmt"
	"maps"
	"math"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // board.timezone loads on systems without a zoneinfo database

	"github.com/Trum-ok/gh-dojo/internal/render"
)

var (
	verbNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	repoRE     = regexp.MustCompile(`^` + RepoPattern + `$`)
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
	if err := c.buildAgent(r.Agent); err != nil {
		return err
	}
	if err := c.buildRelease(r.Release); err != nil {
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
	if r.Timezone != nil {
		loc, err := c.loadTimezone(*r.Timezone)
		if err != nil {
			return err
		}
		b.Timezone, b.Location = *r.Timezone, loc
	}
	c.Board = b
	return nil
}

// loadTimezone validates board.timezone: an IANA time zone name, such as
// "Europe/Moscow" or "UTC". "Local" is refused: it would make "today"
// depend on the machine a verb runs on. The time zone database is built
// into the binary (time/tzdata), so a name loads on every OS.
func (c *Config) loadTimezone(name string) (*time.Location, error) {
	fix := `write an IANA time zone name, like timezone = "Europe/Moscow", or remove it for UTC`
	if strings.TrimSpace(name) == "" || name == "Local" {
		return nil, c.Errorf("board.timezone: %q is not a time zone name", name).WithFix(fix)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, c.Errorf("board.timezone: unknown time zone %q", name).WithFix(fix)
	}
	return loc, nil
}

// RepoPattern matches "owner/name": an owner of letters, digits, dashes
// and underscores (enterprise managed users), not starting with a dash,
// and a name of letters, digits, dots, dashes and underscores. Every
// regexp for a repository is built from it.
const RepoPattern = `[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+`

// IsLogin reports whether s is spelled as a GitHub login: letters,
// digits and dashes, not starting with a dash, at most 39 long.
func IsLogin(s string) bool { return loginRE.MatchString(s) }

// AssigneeArgError is why login, given to the command-line flag, cannot
// be an assignee, or nil for Me or a GitHub login. The caller adds the fix.
func AssigneeArgError(flag, login string) *render.Error {
	switch {
	case login == Me || IsLogin(login):
		return nil
	case strings.HasPrefix(login, "@"):
		return render.Usage("--%s %q: write the login without @, or %s for yourself", flag, login, Me)
	}
	return render.Usage("--%s %q is not a GitHub login; pass a login, or %s for yourself", flag, login, Me)
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
	c.Labels.TypePrefix = deref(r.TypePrefix)
	c.Labels.AreaPrefix = deref(r.AreaPrefix)
	// No label may be both a type and an area. GitHub matches label names
	// regardless of case, so neither prefix may start the other.
	t, a := strings.ToLower(c.Labels.TypePrefix), strings.ToLower(c.Labels.AreaPrefix)
	if t != "" && a != "" && (strings.HasPrefix(t, a) || strings.HasPrefix(a, t)) {
		return c.Errorf("%s %q and %s %q overlap: a label would be both a type and an area",
			c.KeyPath("labels.type_prefix"), c.Labels.TypePrefix, c.KeyPath("labels.area_prefix"), c.Labels.AreaPrefix).
			WithFix("change one prefix in " + c.Display + " so that neither starts the other, such as \"type: \" and \"area: \"")
	}
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

// buildAgent validates [agent]. Notes must be a path inside the repository
// written with slashes, so the generated files are the same on every OS;
// whether the file exists is checked by agent install, which reads it.
func (c *Config) buildAgent(r rawAgent) error {
	c.Agent = Agent{Set: r.Targets != nil || r.Notes != nil || r.Description != nil, Targets: []string{AgentClaude}}
	if r.Targets != nil {
		if err := c.checkList("agent.targets", *r.Targets); err != nil {
			return err
		}
		for _, t := range *r.Targets {
			if !slices.Contains(agentTargets, t) {
				return c.Errorf("agent.targets: unknown agent %q", t).WithAllowed(agentTargets...)
			}
		}
		c.Agent.Targets = slices.Clone(*r.Targets)
	}
	if r.Notes != nil {
		n := *r.Notes
		fix := `write a path relative to the repository root, like notes = "docs/agent-notes.md"`
		switch {
		case strings.TrimSpace(n) == "":
			return c.Errorf("agent.notes is empty").WithFix(fix + ", or remove it")
		case strings.Contains(n, `\`):
			return c.Errorf("agent.notes: %q: separate directories with /", n).WithFix(fix)
		case !filepath.IsLocal(filepath.FromSlash(n)) || path.Clean(n) != n:
			return c.Errorf("agent.notes: %q is not a clean path inside the repository", n).WithFix(fix)
		case strings.EqualFold(strings.Split(n, "/")[0], ".git"):
			return c.Errorf("agent.notes: %q is inside .git", n).WithFix(fix)
		}
		for _, a := range agentTargets {
			if strings.EqualFold(n, agentFiles[a]) {
				return c.Errorf("agent.notes: %q is the file agent install writes for %s", n, a).WithFix(fix)
			}
		}
		c.Agent.Notes = n
	}
	if r.Description != nil {
		d := strings.TrimSpace(*r.Description)
		switch {
		case d == "":
			return c.Errorf("agent.description is empty").WithFix("write when else to load the skill, or remove it")
		case strings.ContainsAny(d, "\r\n"):
			return c.Errorf("agent.description must be one line")
		}
		c.Agent.Description = d
	}
	return nil
}

// keywordRE is a release keyword: words of letters, separated by single
// spaces or dashes ("refs", "part of", "closes").
var keywordRE = regexp.MustCompile(`^\pL+(?:[ -]\pL+)*$`)

// buildRelease validates [release].
func (c *Config) buildRelease(r rawRelease) error {
	c.Release = Release{Keywords: DefaultKeywords(), Suffix: r.Suffix != nil && *r.Suffix}
	if r.Keywords == nil {
		return nil
	}
	if err := c.checkList("release.keywords", *r.Keywords); err != nil {
		return err
	}
	for i, k := range *r.Keywords {
		if !keywordRE.MatchString(k) {
			return c.Errorf("release.keywords: %q is not a word; write words of letters, like \"refs\" or \"part of\"", k).
				WithFix(`keywords = ["closes", "fixes", "refs"]`)
		}
		if j := slices.IndexFunc((*r.Keywords)[:i], func(o string) bool { return strings.EqualFold(o, k) }); j >= 0 {
			return c.Errorf("release.keywords lists %q and %q: keywords match regardless of case", (*r.Keywords)[j], k)
		}
	}
	c.Release.Keywords = slices.Clone(*r.Keywords)
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
	if err := c.buildLabelChecks(name, r, &v); err != nil {
		return Verb{}, err
	}
	if len(v.Effects()) == 0 {
		keys := make([]string, len(verbEffects))
		for i, e := range verbEffects {
			keys[i] = e.key
		}
		return Verb{}, c.Errorf("verbs.%s has no effect: set %s", name, render.JoinWords(keys, "or"))
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
			// An argument an effect takes (assign = "{assignee}",
			// milestone = "{milestone}", close = "duplicate") is one that
			// effect requires.
			effect, fromArg, taken, does := "", "", false, ""
			switch arg {
			case ArgAssignee:
				effect, fromArg, taken, does = "assign", AssignFromArg, v.AssignArg, "assign the user --"+arg+" names"
			case ArgMilestone:
				effect, fromArg, taken, does = "milestone", MilestoneFromArg, v.MilestoneArg, "set the milestone --"+arg+" names"
			case ArgOf:
				effect, fromArg, taken, does = "close", CloseDuplicate, v.ClosesDuplicate(),
					"close the issue as a duplicate of the one --"+arg+" names"
			}
			switch {
			case effect != "" && list.key == "require_one_of" && taken:
				return Verb{}, c.Errorf("%s names %q, which %s = %q always requires", key, arg, c.KeyPath(p+effect), fromArg).
					WithFix(fmt.Sprintf("remove %q from %s", arg, key))
			case effect != "" && !taken:
				return Verb{}, c.Errorf("%s names %q, but %s is not %q", key, arg, c.KeyPath(p+effect), fromArg).
					WithFix(fmt.Sprintf(`set %s = %q to %s, or remove %q from %s`, effect, fromArg, does, arg, key))
			case !v.Takes(arg):
				return Verb{}, c.Errorf("%s names %q, but %s does not use {%s}", key, arg, c.KeyPath(p+"comment"), arg)
			}
		}
		*list.dest = *list.src
	}
	if v.AssignArg && !slices.Contains(v.Require, ArgAssignee) {
		// Nothing else names the user to assign.
		v.Require = append(slices.Clone(v.Require), ArgAssignee)
	}
	if v.MilestoneArg && !slices.Contains(v.Require, ArgMilestone) {
		// Nothing else names the milestone to set.
		v.Require = append(slices.Clone(v.Require), ArgMilestone)
	}
	if v.ClosesDuplicate() && !slices.Contains(v.Require, ArgOf) {
		// Nothing else names the original to link.
		v.Require = append(slices.Clone(v.Require), ArgOf)
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

// buildEffects validates labels, assign, unassign, milestone, close, reopen, confirm, blockers,
// sub_issues, iteration and field.
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
	if err := c.buildAssign(name, r, v); err != nil {
		return err
	}
	if err := c.buildMilestone(name, r.Milestone, v); err != nil {
		return err
	}
	key := c.KeyPath(p + "close")
	switch reason, set, err := c.offString(key, r.Close, "a reason", "leave the issue open", closeReasons...); {
	case err != nil:
		return err
	case set && !slices.Contains(closeReasons, reason):
		return c.Errorf("%s: unknown reason %q", key, reason).WithAllowed(closeReasons...)
	case set:
		v.Close = reason
	}
	v.Reopen = deref(r.Reopen)
	v.Batch = deref(r.Batch)
	if v.Close != "" && v.Reopen {
		return c.Errorf("verbs.%s sets both close and reopen; keep one", name)
	}
	if !v.ClosesDuplicate() && v.Uses(VarOf) {
		return c.Errorf("%s uses {%s}, the --%s issue, but %s is not %q",
			c.KeyPath(p+"comment"), VarOf, ArgOf, c.KeyPath(p+"close"), CloseDuplicate).
			WithFix(fmt.Sprintf(`set close = %q to close the issue as a duplicate of the one --%s names, or drop {%s}`,
				CloseDuplicate, ArgOf, VarOf))
	}
	key = c.KeyPath(p + "confirm")
	switch who, set, err := c.offString(key, r.Confirm, strconv.Quote(ConfirmUser), offTurnsOff, strconv.Quote(ConfirmUser)); {
	case err != nil:
		return err
	case set && who != ConfirmUser:
		return c.Errorf("%s: unknown value %q", key, who).WithAllowed(ConfirmUser)
	case set:
		v.Confirm = true
	}
	if err := c.buildChecks(name, "blockers", r.Blockers, v); err != nil {
		return err
	}
	if err := c.buildChecks(name, "sub_issues", r.SubIssues, v); err != nil {
		return err
	}
	if err := c.buildIteration(name, r.Iteration, v); err != nil {
		return err
	}
	return c.buildFieldValues(name, r.Field, v)
}

// buildLabelChecks validates from_labels and refuse_labels: label names,
// without the + or - of the labels effect, each at most once regardless of
// case (GitHub's label names are unique regardless of case), and none in
// both lists, which would refuse every issue. A checked label cannot be
// in the verb's own labels effect either. One it requires and adds, or
// refuses and removes, is an effect that never changes anything. One it
// refuses and adds, or requires and removes, makes the check refuse the
// verb's own re-run after a partly applied run, which must finish what
// the run left undone. It runs after buildEffects, which fills the labels
// the verb adds and removes.
func (c *Config) buildLabelChecks(name string, r *rawVerb, v *Verb) error {
	p := "verbs." + name + "."
	type named struct{ key, label string }
	seen := map[string]named{} // folded name → the entry that names it
	for _, list := range []struct {
		key  string
		src  *[]string
		dest *[]string
		// moot are the labels effect's entries the check makes moot,
		// with their sign and verb; rerun those that would make it
		// refuse a re-run.
		moot, rerun                   []string
		mootSign, rerunSign, mootDoes string
	}{
		{"from_labels", r.FromLabels, &v.FromLabels, v.AddLabels, v.RemoveLabels, "+", "-", "adds"},
		{"refuse_labels", r.RefuseLabels, &v.RefuseLabels, v.RemoveLabels, v.AddLabels, "-", "+", "removes"},
	} {
		if list.src == nil {
			continue
		}
		key := c.KeyPath(p + list.key)
		if err := c.checkList(p+list.key, *list.src); err != nil {
			return err
		}
		for _, label := range *list.src {
			if label[0] == '+' || label[0] == '-' {
				// The name without the sign, if that is a name at all.
				fix := fmt.Sprintf(`%s = [%q]`, list.key, strings.TrimLeft(label, "+-"))
				if nameProblem(strings.TrimLeft(label, "+-")) != "" {
					fix = fmt.Sprintf(`write a label name, like %s = ["blocked"]`, list.key)
				}
				return c.Errorf("%s: %q: write the label name without %c; + and - mark labels to add and remove", key, label, label[0]).
					WithFix(fix)
			}
			folded := strings.ToLower(label)
			switch prev, ok := seen[folded]; {
			case ok && prev.key == key:
				return c.Errorf("%s lists %q and %q: label names match regardless of case; keep one", key, prev.label, label)
			case ok:
				return c.Errorf("%s and %s both name %q, so the verb would refuse every issue; keep one",
					prev.key, key, label)
			}
			seen[folded] = named{key, label}
			same := func(l string) bool { return strings.EqualFold(l, label) }
			labelsKey := c.KeyPath(p + "labels")
			if i := slices.IndexFunc(list.moot, same); i >= 0 {
				entry := list.mootSign + list.moot[i]
				return c.Errorf("%s names %q, so %s %s never %s anything; keep one",
					key, label, labelsKey, entry, list.mootDoes).
					WithFix(fmt.Sprintf("remove %q from %s, or %q from %s", entry, labelsKey, label, key))
			}
			if i := slices.IndexFunc(list.rerun, same); i >= 0 {
				does, again := "adds", "adding the label again changes nothing"
				if list.rerunSign == "-" {
					does, again = "removes", "removing a label the issue lacks changes nothing"
				}
				return c.Errorf("%s names %q, which %s %s (%s%s): the check would refuse a re-run that finishes a partly applied run",
					key, label, labelsKey, does, list.rerunSign, list.rerun[i]).
					WithFix(fmt.Sprintf("remove %q from %s; %s", label, key, again))
			}
		}
		*list.dest = slices.Clone(*list.src)
	}
	return nil
}

// buildAssign validates assign and unassign. assign is true
// (board.assignee), false, a login ("@me" or a user) or "{assignee}", the
// --assignee user; unassign the same but "{assignee}", or "all" (any
// case; a user named all cannot be named). The two cannot name the same
// user: the verb never removes whom it assigns.
func (c *Config) buildAssign(name string, r *rawVerb, v *Verb) error {
	p := "verbs." + name + "."
	user := func(k string, a *rawAssign) (string, error) {
		key := c.KeyPath(p + k)
		switch {
		case a == nil || a.Bool != nil && !*a.Bool:
			return "", nil
		case a.Bool != nil && c.Board.Assignee == "":
			return "", c.Errorf("%s = true %ss board.assignee, which is not set", key, k).
				WithFix(fmt.Sprintf(`add assignee = %q under [board] in %s, or set %s = %q`, Me, c.Display, k, Me))
		case a.Bool != nil:
			return c.Board.Assignee, nil
		case k == "assign" && *a.User == AssignFromArg:
			return *a.User, nil
		case k == "unassign" && strings.EqualFold(*a.User, UnassignAll):
			return UnassignAll, nil
		case k == "assign" && strings.EqualFold(*a.User, UnassignAll):
			return "", c.Errorf("%s = %q: assign adds one user; %q is a value of unassign", key, *a.User, UnassignAll).
				WithFix(fmt.Sprintf(`to leave the issue to the user this verb assigns, set unassign = %q next to assign`, UnassignAll))
		case k == "unassign" && *a.User == AssignFromArg:
			return "", c.Errorf("%s = %q: a verb removes only users the config names", key, *a.User).
				WithAllowed("true", "false", strconv.Quote(Me), "a login", strconv.Quote(UnassignAll))
		case strings.HasPrefix(*a.User, "{"):
			allowed := []string{"true", "false", strconv.Quote(Me), "a login"}
			if k == "assign" {
				allowed = append(allowed, strconv.Quote(AssignFromArg))
			} else {
				allowed = append(allowed, strconv.Quote(UnassignAll))
			}
			return "", c.Errorf("%s: unknown value %q", key, *a.User).WithAllowed(allowed...)
		}
		if err := c.checkAssignee(p+k, *a.User); err != nil {
			return "", err
		}
		return *a.User, nil
	}
	assign, err := user("assign", r.Assign)
	if err != nil {
		return err
	}
	if assign == AssignFromArg {
		v.AssignArg = true
	} else {
		v.Assign = assign
	}
	if v.Unassign, err = user("unassign", r.Unassign); err != nil {
		return err
	}
	if v.Assign != "" && strings.EqualFold(v.Assign, v.Unassign) {
		return c.Errorf("verbs.%s assigns and unassigns %s; keep one", name, v.Assign)
	}
	if !v.AssignArg && v.Uses(VarAssignee) {
		return c.Errorf("%s uses {%s}, the --%s user, but %s is not %q",
			c.KeyPath(p+"comment"), VarAssignee, ArgAssignee, c.KeyPath(p+"assign"), AssignFromArg).
			WithFix(fmt.Sprintf(`set assign = %q to assign the user --%s names, or drop {%s}`, AssignFromArg, ArgAssignee, VarAssignee))
	}
	return nil
}

// buildMilestone validates the milestone key: the title of a milestone
// (an open one of the issue's repository, matched when the verb runs, as
// doctor checks it), "{milestone}" for the --milestone one, or false to
// turn off a preset's. A title is one line without surrounding spaces and
// without placeholders; {milestone} only stands alone.
func (c *Config) buildMilestone(name string, o *rawOff, v *Verb) error {
	p := "verbs." + name + "."
	key := c.KeyPath(p + "milestone")
	m, set, err := c.offString(key, o, fmt.Sprintf("a milestone title, %q for the --%s one", MilestoneFromArg, ArgMilestone),
		offTurnsOff, "a milestone title", strconv.Quote(MilestoneFromArg))
	if err != nil {
		return err
	}
	if set {
		fix := fmt.Sprintf(`write the title of an open milestone, like milestone = "v1.2", or milestone = %q to take it from --%s`,
			MilestoneFromArg, ArgMilestone)
		switch {
		case m == MilestoneFromArg:
			v.MilestoneArg = true
		case strings.TrimSpace(m) == "":
			return c.Errorf("%s is empty", key).WithFix(fix)
		case placeholderRE.MatchString(m):
			if ph := placeholderRE.FindStringSubmatch(m)[1]; ph != ArgMilestone {
				return c.Errorf("%s: unknown placeholder {%s}", key, ph).
					WithAllowed("a milestone title", strconv.Quote(MilestoneFromArg))
			}
			return c.Errorf("%s = %q: {%s} is the whole value, for the milestone --%s names", key, m, ArgMilestone, ArgMilestone).
				WithFix(fmt.Sprintf(`milestone = %q`, MilestoneFromArg))
		case strings.ContainsAny(m, "\r\n"):
			return c.Errorf("%s must be one line", key)
		case nameProblem(m) != "":
			return c.Errorf("%s %s", key, nameProblem(m))
		default:
			v.Milestone = m
		}
	}
	if !v.MilestoneArg && v.Uses(VarMilestone) {
		return c.Errorf("%s uses {%s}, the --%s milestone, but %s is not %q",
			c.KeyPath(p+"comment"), VarMilestone, ArgMilestone, key, MilestoneFromArg).
			WithFix(fmt.Sprintf(`set milestone = %q to set the milestone --%s names, or drop {%s}`,
				MilestoneFromArg, ArgMilestone, VarMilestone))
	}
	return nil
}

// buildChecks validates the blockers or the sub_issues key (k): "warn" or
// "deny" (checks of the issue's unresolved blockers or sub-issues), for
// blockers also "link" (the effect, which needs {by} in the comment
// template), or false to turn off a preset's.
func (c *Config) buildChecks(name, k string, o *rawOff, v *Verb) error {
	p := "verbs." + name + "."
	key := c.KeyPath(p + k)
	values, dest, what := blockersValues, &v.Blockers, "blockers"
	if k == "sub_issues" {
		values, dest, what = subIssuesValues, &v.SubIssues, "sub-issues"
	}
	quoted := make([]string, len(values))
	for i, val := range values {
		quoted[i] = strconv.Quote(val)
	}
	b, set, err := c.offString(key, o, render.JoinWords(quoted, "or"), offTurnsOff, quoted...)
	switch {
	case err != nil:
		return err
	case !set:
		// Unset, or false: no check and no link, turning off a preset's.
	case !slices.Contains(values, b):
		check := fmt.Sprintf("%q and %q check the issue's unresolved %s", BlockersWarn, BlockersDeny, what)
		if k == "blockers" {
			check += fmt.Sprintf(", %q marks the issue blocked by the --by issue", BlockersLink)
		}
		return c.Errorf("%s: unknown value %q: %s", key, b, check).WithAllowed(values...)
	case b == BlockersLink && !v.Uses(ArgBy):
		return c.Errorf("%s = %q takes the blocking issue from --by, but %s does not use {%s}",
			key, b, c.KeyPath(p+"comment"), ArgBy).
			WithFix(fmt.Sprintf(`add {%s} to %s, e.g. "Waiting for {%s}.", or set %s = false to drop the link`,
				ArgBy, c.KeyPath(p+"comment"), ArgBy, p+k))
	default:
		*dest = b
	}
	return nil
}

// offTurnsOff is what false does to a key that takes a string or false,
// but close (see offString).
const offTurnsOff = "turn it off"

// offString reads key, a verb's key that takes a string or false (rawOff):
// it returns the string and true when one is set, and false when the key
// is unset or false, which turns off a preset's value. true is an error:
// "<key> = true: want <want>, or false to <off>", allowing allowed and
// false.
func (c *Config) offString(key string, o *rawOff, want, off string, allowed ...string) (string, bool, error) {
	switch {
	case o == nil || o.Bool != nil && !*o.Bool:
		return "", false, nil
	case o.Bool != nil:
		return "", false, c.Errorf("%s = true: want %s, or false to %s", key, want, off).
			WithAllowed(slices.Concat(allowed, []string{"false"})...)
	}
	return *o.Str, true, nil
}

// buildIteration validates the iteration key: "current", "clear" or
// "by_status_date:<status>" ("" sets nothing, to turn off a preset's), and
// that fields.sprint names the field it sets. by_status_date needs the
// built-in status field (BuiltinStatus): GitHub keeps no history for
// another. The status is checked against the board with the others (see
// StatusRefs).
func (c *Config) buildIteration(name string, raw *string, v *Verb) error {
	if raw == nil || *raw == "" {
		return nil
	}
	key := c.KeyPath("verbs." + name + ".iteration")
	val := *raw
	status, byDate := strings.CutPrefix(val, IterationByStatusDate+":")
	switch {
	case val == IterationCurrent, val == IterationClear:
		v.Iteration = val
	case byDate:
		if problem := nameProblem(status); problem != "" {
			return c.Errorf("%s: %q: the status %s", key, val, problem).
				WithFix(fmt.Sprintf(`iteration = "%s:Ready" picks the iteration of the day the issue entered Ready`, IterationByStatusDate))
		}
		if f := c.StatusField(); f != BuiltinStatus {
			return c.Errorf("%s: %q needs the board's built-in %s field, but fields.%s is %q: "+
				"GitHub keeps a status history only for %s", key, val, BuiltinStatus, statusField, f, BuiltinStatus).
				WithFix(fmt.Sprintf(`set iteration = %q in verbs.%s`, IterationCurrent, name))
		}
		v.Iteration, v.IterationStatus = IterationByStatusDate, status
	default:
		return c.Errorf("%s: unknown value %q", key, val).
			WithAllowed(IterationCurrent, IterationClear, IterationByStatusDate+":<status>")
	}
	if c.SprintField() == "" {
		return c.Errorf("%s sets the sprint, but fields.%s is not set", key, sprintField).
			WithFix(fmt.Sprintf(`add %s = "<iteration field>" under [fields] in %s`, sprintField, c.Display))
	}
	return nil
}

// buildFieldValues validates the field table: board field names with a
// string, number or date each. Whether the board has the field and the
// value fits its type is checked against the board (board.Problems).
func (c *Config) buildFieldValues(name string, raw *rawFields, v *Verb) error {
	if raw == nil {
		return nil
	}
	if len(raw.values) == 0 {
		return c.Errorf("%s is empty: name a board field and its value, like Priority = \"P1\"",
			c.KeyPath("verbs."+name+".field"))
	}
	for _, field := range slices.Sorted(maps.Keys(raw.values)) {
		key := c.KeyPath(FieldKey(name, field))
		if problem := nameProblem(field); problem != "" {
			return c.Errorf("%s: the field name %s", key, problem)
		}
		fv := FieldValue{Field: field}
		switch val := raw.values[field].(type) {
		case string:
			if problem := nameProblem(val); problem != "" {
				return c.Errorf("%s %s", key, problem)
			}
			fv.Text = val
		case int64:
			fv.Text, fv.Number = strconv.FormatInt(val, 10), true
		case float64:
			if math.IsNaN(val) || math.IsInf(val, 0) {
				return c.Errorf("%s: %v is not a number a field can hold", key, val)
			}
			fv.Text, fv.Number = strconv.FormatFloat(val, 'f', -1, 64), true
		case time.Time:
			// The decoder gives a local date (Due = 2026-11-30) this
			// location; a date-time has another one.
			if val.Location().String() != tomlLocalDate {
				return c.Errorf("%s: write a date as YYYY-MM-DD, without a time", key)
			}
			fv.Text, fv.Date = val.Format(time.DateOnly), true
		default:
			return c.Errorf("%s: wrong type: want a string (an option or a text), a number or a date (YYYY-MM-DD)", key)
		}
		switch field {
		case c.StatusField():
			return c.Errorf("%s sets the status field; set the status with to", key)
		case c.SprintField():
			return c.Errorf("%s sets the sprint field; set the sprint with iteration", key)
		}
		v.Fields = append(v.Fields, fv)
	}
	return nil
}

// tomlLocalDate is the name of the location BurntSushi/toml decodes a
// local date (no time, no offset) into.
const tomlLocalDate = "date-local"

// bareKeyRE matches a TOML key that needs no quotes.
var bareKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// FieldKey is the key path of a board field that verb sets in its field
// table: verbs.triage.field.Priority, or verbs.triage.field."Story points"
// for a name that needs quotes in TOML.
func FieldKey(verb, field string) string {
	if !bareKeyRE.MatchString(field) {
		field = strconv.Quote(field)
	}
	return "verbs." + verb + ".field." + field
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
