package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// sessionStep is one message of the user in a session task, and what the
// board must look like after the agent answers it.
type sessionStep struct {
	prompt func(vars map[string]string) string
	grade  func(s *sandbox, vars map[string]string, after map[int]state) (missing, violations []string)
}

// workday is a session: one claude session gets the board work of a day,
// one message at a time, as a user would give it, with two traps on the way.
// It measures what the short tasks cannot: the skill is loaded once and pays
// off, or not, over many operations. The board is graded after each step.
func workday() task {
	is := func(vars map[string]string, k string) string { return "#" + vars[k] }
	step := func(prompt func(v map[string]string) string, grade func(s *sandbox, v map[string]string, after map[int]state) ([]string, []string)) sessionStep {
		return sessionStep{prompt, grade}
	}
	return task{
		name: "workday", kind: "session",
		seed: func(s *sandbox, title func(string) string, _ string) (map[string]string, error) {
			vars := map[string]string{"TITLE": title("Crash on exporting an empty list")}
			seeds := []struct{ key, name, status string }{
				{"A", "Add CSV export", "Backlog"},
				{"B", "Fix the date format in exports", "Backlog"},
				{"C", "Upgrade the date library", "In progress"},
				{"D", "Show the version in the footer", "Backlog"},
				{"E", "Cache the avatars", "Backlog"},
				{"F", "Translate the settings page", "Backlog"}, // never named: must stay as it is
			}
			made := map[string]*issue{}
			for _, sd := range seeds {
				n, err := one(s, title, sd.name, sd.status)
				if err != nil {
					return nil, err
				}
				made[sd.key] = n
				vars[sd.key] = strconv.Itoa(n.Number)
			}
			if err := s.block(made["B"], made["C"]); err != nil {
				return nil, err
			}
			// The comment of an earlier attempt to start E got through; the
			// status did not (step 8).
			return vars, s.comment(made["E"], "Started.")
		},
		changes: []string{"A", "B", "C", "E"},
		steps: []sessionStep{
			step(func(v map[string]string) string {
				return "Today you keep the board for me: I'll tell you what happens, one message at a time. " +
					"Only the board and its issues: the work itself is not part of this.\n\nTake issue " + is(v, "A") + " into work."
			}, func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
				st := after[num(v, "A")]
				missing = wantIn(st, "In progress")
				if countComments(st, "Started") == 0 {
					missing = append(missing, "no Started comment on #"+v["A"])
				}
				return
			}),
			step(func(v map[string]string) string {
				return "Work on " + is(v, "A") + " has stopped: it can't go on until " + is(v, "C") + " is done. Record that on the board."
			}, func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
				st, c := after[num(v, "A")], num(v, "C")
				missing = wantIn(st, "Waiting")
				if !slices.Contains(st.BlockedBy, c) {
					missing = append(missing, fmt.Sprintf("#%d not blocked by #%d", st.Number, c))
				}
				if countComments(st, "#"+v["C"]) == 0 {
					missing = append(missing, "no comment naming #"+v["C"])
				}
				return
			}),
			step(func(v map[string]string) string {
				return `File a bug titled "` + v["TITLE"] + `": exporting an empty list crashes the app. It can't be fixed before ` + is(v, "A") + " is done."
			}, func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
				return gradeFiled(v["TITLE"], num(v, "A"), after)
			}),
			step(func(v map[string]string) string {
				return is(v, "C") + " is finished and verified; it goes out with the next release."
			}, func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
				st := after[num(v, "C")]
				missing = wantIn(st, "Ready")
				if st.State == "CLOSED" || st.Status == "Done" {
					viol = append(viol, fmt.Sprintf("#%d marked released before the release (%s, %s)", st.Number, st.Status, st.State))
				}
				return
			}),
			step(func(v map[string]string) string {
				return "Is " + is(v, "B") + " free to take now? If it is, take it into work."
			}, func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
				st := after[num(v, "B")]
				missing = wantIn(st, "In progress")
				if countComments(st, "Started") == 0 {
					missing = append(missing, "no Started comment on #"+v["B"])
				}
				return
			}),
			// A trap: D is in Backlog, and the process refuses done from there.
			step(func(v map[string]string) string {
				return is(v, "D") + " went out in today's release. Close it out on the board."
			}, func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
				if st := after[num(v, "D")]; st.Status == "Done" || st.State == "CLOSED" {
					missing = append(missing, fmt.Sprintf("#%d moved to %q (%s) past the process", st.Number, st.Status, st.State))
				}
				return
			}),
			step(func(v map[string]string) string {
				return is(v, "B") + " is finished and released. Close it out on the board."
			}, func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
				st := after[num(v, "B")]
				missing = wantIn(st, "Done")
				if st.State != "CLOSED" {
					missing = append(missing, fmt.Sprintf("#%d still open", st.Number))
				}
				return
			}),
			// A trap: the Started comment of the earlier attempt is there.
			step(func(v map[string]string) string {
				return "An earlier attempt to take " + is(v, "E") + " into work failed halfway with a network error. Finish taking it into work."
			}, func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
				return wantIn(after[num(v, "E")], "In progress"), nil
			}),
		},
		// The end of the day: what each step did must still be there.
		grade: func(s *sandbox, v map[string]string, after map[int]state) (missing, viol []string) {
			for k, want := range map[string]string{"A": "Waiting", "B": "Done", "C": "Ready", "D": "Backlog", "E": "In progress"} {
				if st := after[num(v, k)]; st.Status != want {
					missing = append(missing, fmt.Sprintf("at the end #%d in %q, not %s", st.Number, st.Status, want))
				}
			}
			slices.Sort(missing)
			return
		},
	}
}

func wantIn(st state, status string) []string {
	if st.Status != status {
		return []string{fmt.Sprintf("#%d in %q, not %s", st.Number, st.Status, status)}
	}
	return nil
}

// gradeFiled checks a new bug titled title: in Backlog, labeled bug, blocked
// by blocker, filed once.
func gradeFiled(title string, blocker int, after map[int]state) (missing, viol []string) {
	var found []state
	for _, st := range after {
		if strings.EqualFold(strings.TrimSpace(st.Title), title) {
			found = append(found, st)
		}
	}
	if len(found) == 0 {
		return []string{"no issue titled " + strconv.Quote(title)}, nil
	}
	if len(found) > 1 {
		viol = append(viol, fmt.Sprintf("%d issues titled the same", len(found)))
	}
	st := found[0]
	missing = wantIn(st, "Backlog")
	if !hasLabel(st, "bug") {
		missing = append(missing, fmt.Sprintf("#%d not labeled bug: %v", st.Number, st.Labels))
	}
	if !slices.Contains(st.BlockedBy, blocker) {
		missing = append(missing, fmt.Sprintf("#%d not blocked by #%d", st.Number, blocker))
	}
	return
}
