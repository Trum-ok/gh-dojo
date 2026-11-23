package release

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Version is a release tag read as a semantic version: v1.2.3, 1.2.3,
// v1.2.3-rc.1; build metadata (+...) is ignored.
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // the prerelease identifiers, nil for a release
}

var versionRE = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
	`(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// ParseVersion reads a tag name ("v1.2.3", also "refs/tags/v1.2.3") as a
// version; ok is false for any other name.
func ParseVersion(tag string) (v Version, ok bool) {
	m := versionRE.FindStringSubmatch(strings.TrimPrefix(tag, "refs/tags/"))
	if m == nil {
		return Version{}, false
	}
	nums := [3]int{}
	for i := range nums {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Version{}, false
		}
		nums[i] = n
	}
	v = Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	return v, true
}

// Prerelease reports whether v is a prerelease (v1.2.3-rc.1).
func (v Version) Prerelease() bool { return len(v.Pre) > 0 }

// Compare orders versions as semantic versioning does: by major, minor and
// patch, a prerelease before its release, prereleases by their
// identifiers (numbers by value, below words).
func (v Version) Compare(o Version) int {
	if c := cmp.Compare(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		a, aErr := strconv.Atoi(v.Pre[i])
		b, bErr := strconv.Atoi(o.Pre[i])
		var c int
		switch {
		case aErr == nil && bErr == nil:
			c = cmp.Compare(a, b)
		case aErr == nil:
			c = -1
		case bErr == nil:
			c = 1
		default:
			c = strings.Compare(v.Pre[i], o.Pre[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(v.Pre), len(o.Pre))
}

// Tag is a tag of the repository and the commit it points at.
type Tag struct {
	Name   string
	Commit string // the commit's oid; "" when the tag points at no commit
}

// Previous picks the release before head, the <ref> of gh dojo release,
// among tags: the highest version tag that is lower than head when head
// is a version tag itself, else the highest version tag; a prerelease only
// when head is a prerelease. Tags on headCommit, head's commit, are not
// before it. ok is false when no tag qualifies: the first release.
//
// A release compares with the previous release, not the previous
// prerelease, so that v0.2.0 includes the issues of v0.2.0-rc.1 as well:
// a verb in place costs no write, while a range too short would miss
// issues.
func Previous(tags []Tag, head, headCommit string) (Tag, bool) {
	c := Candidates(tags, head, headCommit)
	if len(c) == 0 {
		return Tag{}, false
	}
	return c[0], true
}

// Candidates are the tags Previous chooses from, the highest version
// first: the first is Previous. Which of them are in head's history is not
// known here (a backport tagged on a release branch is not), see Read.
func Candidates(tags []Tag, head, headCommit string) []Tag {
	hv, headIsVersion := ParseVersion(head)
	type cand struct {
		tag Tag
		v   Version
	}
	var cands []cand
	for _, t := range tags {
		v, ok := ParseVersion(t.Name)
		switch {
		case !ok || t.Commit == "" || t.Commit == headCommit:
			continue
		case headIsVersion && (v.Compare(hv) >= 0 || v.Prerelease() && !hv.Prerelease()):
			continue
		case !headIsVersion && v.Prerelease():
			continue
		}
		cands = append(cands, cand{t, v})
	}
	// Stable: of equal versions (v1.0.0 and 1.0.0) the first read stays first.
	slices.SortStableFunc(cands, func(a, b cand) int { return b.v.Compare(a.v) })
	out := make([]Tag, len(cands))
	for i, c := range cands {
		out[i] = c.tag
	}
	return out
}
