package issue

import "encoding/json"

// cardQuery reads a whole card in one request (principle 6). Windows:
// labels 20 (100 with Lookup.AllLabels), assignees 10, blockers 20 each way
// (blockedBy 50 with Lookup.AllBlockers; each with its first 10 project
// items), the parent and 20 sub-issues (100 with Lookup.AllSubIssues),
// read as blockers are, project items 20 (each with up to 100 field
// values), the last 20 status changes and the last $comments comments;
// every list of the card keeps its totalCount, so the reader can tell
// "none" from "not read".
//
// A linked issue reads only its status on each of its first 10 projects,
// and how many projects it is on: the reader keeps the one on the
// configured board (Link.Status), which tells a blocker that is done but
// still open (kanban: Ready) from one that blocks, and says when the board
// may be beyond the 10 read (Link.StatusUnknown).
//
// Each project item also reads the value of the field named $status
// directly (fieldValueByName), so the status is there whatever the size of
// the fieldValues window; the reader trusts it only when its field ID is
// the configured status field's, and otherwise looks the ID up among
// fieldValues (a field renamed since the schema was cached). GitHub has no
// lookup by field ID. The item's createdAt bounds its status history.
//
// issueOrPullRequest tells a pull request from a missing number.
// stateReason(enableDuplicate: true) reports DUPLICATE instead of
// NOT_PLANNED. Status changes come from the timeline across all projects;
// the reader keeps those of the configured board.
//
// A Lookup adds fields to it (see lookupQuery).
const cardQuery = cardHead + cardRepo + "\n  }\n}\n\n" + cardFragments

// cardSelection is what a card reads of an issueOrPullRequest: cardQuery
// selects it in place and batchQuery (ReadAll) as "fragment card", so both
// read the same fields.
const cardSelection = `{
      __typename
      ... on Issue {
        id
        number
        title
        state
        stateReason(enableDuplicate: true)
        url
        createdAt
        closedAt
        author { login }
        body
        milestone { title dueOn }
        labels(first: 20) { totalCount nodes { name } }
        assignees(first: 10) { totalCount nodes { login } }
        comments(last: $comments) {
          totalCount
          nodes { id author { login } createdAt viewerDidAuthor body }
        }
        blockedBy(first: 20) { totalCount nodes { ...link } }
        blocking(first: 20) { totalCount nodes { ...link } }
        parent { ...link }
        subIssues(first: 20) { totalCount nodes { ...link } }
        projectItems(first: 20) {
          totalCount
          nodes {
            id
            isArchived
            createdAt
            project { id }
            status: fieldValueByName(name: $status) { ...value }
            fieldValues(first: 100) { totalCount nodes { ...value } }
          }
        }
        timelineItems(last: 20, itemTypes: [PROJECT_V2_ITEM_STATUS_CHANGED_EVENT]) {
          totalCount
          nodes {
            ... on ProjectV2ItemStatusChangedEvent {
              createdAt
              previousStatus
              status
              wasAutomated
              actor { login }
              project { id }
            }
          }
        }
      }
    }`

// cardFragments are the fragments cardSelection spreads.
const cardFragments = `fragment link on Issue {
  number
  title
  state
  repository { nameWithOwner }
  projectItems(first: 10) {
    totalCount
    nodes {
      isArchived
      project { id }
      status: fieldValueByName(name: $status) {
        ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2FieldCommon { id } } }
      }
    }
  }
}

fragment value on ProjectV2ItemFieldValue {
  __typename
  ... on ProjectV2ItemFieldSingleSelectValue { name optionId field { ...field } }
  ... on ProjectV2ItemFieldIterationValue { title iterationId startDate duration field { ...field } }
  ... on ProjectV2ItemFieldTextValue { text field { ...field } }
  ... on ProjectV2ItemFieldNumberValue { number field { ...field } }
  ... on ProjectV2ItemFieldDateValue { date field { ...field } }
}

fragment field on ProjectV2FieldCommon { id name dataType }`

// historyWindow is the timelineItems(last: N) of cardQuery.
const historyWindow = 20

type cardResponse struct {
	Repository *repoJSON
	Viewer     *User // read for Lookup.Viewer only
	// data is the whole "data" object, for the links of a Lookup.
	data map[string]json.RawMessage
}

func (r *cardResponse) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &r.data); err != nil {
		return err
	}
	if raw, ok := r.data["repository"]; ok {
		if err := json.Unmarshal(raw, &r.Repository); err != nil {
			return err
		}
	}
	if raw, ok := r.data["viewer"]; ok {
		if err := json.Unmarshal(raw, &r.Viewer); err != nil {
			return err
		}
	}
	return nil
}

type login struct {
	ID    string `json:"id"` // read only for the assignees with Lookup.AssigneeIDs
	Login string `json:"login"`
}

type conn[T any] struct {
	TotalCount int `json:"totalCount"`
	Nodes      []T `json:"nodes"`
}

type issueJSON struct {
	Typename    string  `json:"__typename"`
	ID          string  `json:"id"`
	Number      int     `json:"number"`
	Title       string  `json:"title"`
	State       string  `json:"state"`
	StateReason *string `json:"stateReason"`
	URL         string  `json:"url"`
	CreatedAt   string  `json:"createdAt"`
	ClosedAt    *string `json:"closedAt"`
	Author      *login  `json:"author"`
	Body        string  `json:"body"`
	Milestone   *struct {
		Title string  `json:"title"`
		DueOn *string `json:"dueOn"`
	} `json:"milestone"`
	Labels conn[struct {
		Name string `json:"name"`
	}] `json:"labels"`
	Assignees conn[login] `json:"assignees"`
	Comments  conn[struct {
		ID              string `json:"id"`
		Author          *login `json:"author"`
		CreatedAt       string `json:"createdAt"`
		ViewerDidAuthor bool   `json:"viewerDidAuthor"`
		Body            string `json:"body"`
	}] `json:"comments"`
	BlockedBy     conn[*linkJSON] `json:"blockedBy"`
	Blocking      conn[*linkJSON] `json:"blocking"`
	Parent        *linkJSON       `json:"parent"`
	SubIssues     conn[*linkJSON] `json:"subIssues"`
	ProjectItems  conn[*itemJSON] `json:"projectItems"`
	TimelineItems conn[*struct {
		CreatedAt      string `json:"createdAt"`
		PreviousStatus string `json:"previousStatus"`
		Status         string `json:"status"`
		WasAutomated   bool   `json:"wasAutomated"`
		Actor          *login `json:"actor"`
		Project        *struct {
			ID string `json:"id"`
		} `json:"project"`
	}] `json:"timelineItems"`
}

type itemJSON struct {
	ID         string `json:"id"`
	IsArchived bool   `json:"isArchived"`
	CreatedAt  string `json:"createdAt"`
	Project    *struct {
		ID string `json:"id"`
	} `json:"project"`
	Status      *valueJSON       `json:"status"`
	FieldValues conn[*valueJSON] `json:"fieldValues"`
}

type linkJSON struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Repository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	ProjectItems conn[*struct {
		IsArchived bool `json:"isArchived"`
		Project    *struct {
			ID string `json:"id"`
		} `json:"project"`
		Status *struct {
			Name  string `json:"name"`
			Field *struct {
				ID string `json:"id"`
			} `json:"field"`
		} `json:"status"`
	}] `json:"projectItems"`
}

type valueJSON struct {
	Typename    string   `json:"__typename"`
	Name        string   `json:"name"`
	OptionID    string   `json:"optionId"`
	Title       string   `json:"title"`
	IterationID string   `json:"iterationId"`
	StartDate   string   `json:"startDate"`
	Duration    int      `json:"duration"`
	Text        string   `json:"text"`
	Number      *float64 `json:"number"`
	Date        string   `json:"date"`
	Field       *struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		DataType string `json:"dataType"`
	} `json:"field"`
}
