package issue

// cardQuery reads a whole card in one request (principle 6). Windows:
// labels 20, assignees 10, blockers 20 each way, project items 20 (each with
// up to 50 field values), the last 20 status changes and the last $comments
// comments; every list keeps its totalCount.
//
// issueOrPullRequest tells a pull request from a missing number.
// stateReason(enableDuplicate: true) reports DUPLICATE instead of
// NOT_PLANNED. Status changes come from the timeline across all projects;
// the reader keeps those of the configured board.
const cardQuery = `query IssueCard($owner: String!, $repo: String!, $number: Int!, $comments: Int!) {
  repository(owner: $owner, name: $repo) {
    issueOrPullRequest(number: $number) {
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
        labels(first: 20) { totalCount nodes { name } }
        assignees(first: 10) { totalCount nodes { login } }
        comments(last: $comments) {
          totalCount
          nodes { id author { login } createdAt viewerDidAuthor body }
        }
        blockedBy(first: 20) { totalCount nodes { ...link } }
        blocking(first: 20) { totalCount nodes { ...link } }
        projectItems(first: 20) {
          totalCount
          nodes {
            id
            isArchived
            project { id }
            fieldValues(first: 50) {
              nodes {
                __typename
                ... on ProjectV2ItemFieldSingleSelectValue { name optionId field { ...field } }
                ... on ProjectV2ItemFieldIterationValue { title iterationId startDate duration field { ...field } }
                ... on ProjectV2ItemFieldTextValue { text field { ...field } }
                ... on ProjectV2ItemFieldNumberValue { number field { ...field } }
                ... on ProjectV2ItemFieldDateValue { date field { ...field } }
              }
            }
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
    }
  }
}

fragment link on Issue { number title state repository { nameWithOwner } }

fragment field on ProjectV2FieldCommon { id name dataType }`

// historyWindow is the timelineItems(last: N) of cardQuery.
const historyWindow = 20

type cardResponse struct {
	Repository *struct {
		IssueOrPullRequest *issueJSON `json:"issueOrPullRequest"`
	} `json:"repository"`
}

type login struct {
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
	Labels      conn[struct {
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
	BlockedBy    conn[*linkJSON] `json:"blockedBy"`
	Blocking     conn[*linkJSON] `json:"blocking"`
	ProjectItems conn[*struct {
		ID         string `json:"id"`
		IsArchived bool   `json:"isArchived"`
		Project    *struct {
			ID string `json:"id"`
		} `json:"project"`
		FieldValues conn[*valueJSON] `json:"fieldValues"`
	}] `json:"projectItems"`
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

type linkJSON struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Repository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
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
