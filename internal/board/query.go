package board

// schemaQuery reads everything Schema holds in one request. repositoryOwner
// resolves both users and organizations, so the owner type needn't be known
// up front. A project has at most 50 fields, so one page of 100 is enough.
const schemaQuery = `query BoardSchema($owner: String!, $number: Int!) {
  repositoryOwner(login: $owner) {
    __typename
    login
    ... on User { projectV2(number: $number) { ...board } }
    ... on Organization { projectV2(number: $number) { ...board } }
  }
}

fragment board on ProjectV2 {
  id
  title
  url
  closed
  fields(first: 100) {
    nodes {
      ... on ProjectV2FieldCommon { id name dataType }
      ... on ProjectV2SingleSelectField { options { id name } }
      ... on ProjectV2IterationField {
        configuration {
          iterations { id title startDate duration }
          completedIterations { id title startDate duration }
        }
      }
    }
  }
}`

type schemaResponse struct {
	RepositoryOwner *struct {
		Typename  string             `json:"__typename"`
		Login     string             `json:"login"`
		ProjectV2 *schemaProjectNode `json:"projectV2"`
	} `json:"repositoryOwner"`
}

// schemaProjectNode is the project as the schema query reads it, with its
// fields (projectNode is one as ListProjects lists it).
type schemaProjectNode struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Closed bool   `json:"closed"`
	Fields struct {
		Nodes []fieldNode `json:"nodes"`
	} `json:"fields"`
}

type fieldNode struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	DataType      FieldType `json:"dataType"`
	Options       []Option  `json:"options"`
	Configuration *struct {
		Iterations          []Iteration `json:"iterations"`
		CompletedIterations []Iteration `json:"completedIterations"`
	} `json:"configuration"`
}

func (p *schemaProjectNode) schema(ownerType, login string, number int) *Schema {
	s := &Schema{
		Owner:     login,
		OwnerType: ownerType,
		Number:    number,
		ID:        p.ID,
		Title:     p.Title,
		URL:       p.URL,
		Closed:    p.Closed,
		Fields:    make([]Field, 0, len(p.Fields.Nodes)),
	}
	for _, n := range p.Fields.Nodes {
		if n.ID == "" {
			continue // a field type this query doesn't know
		}
		f := Field{ID: n.ID, Name: n.Name, Type: n.DataType, Options: n.Options}
		if c := n.Configuration; c != nil {
			f.Iterations = append(f.Iterations, c.Iterations...)
			for _, it := range c.CompletedIterations {
				it.Completed = true
				f.Iterations = append(f.Iterations, it)
			}
		}
		s.Fields = append(s.Fields, f)
	}
	return s
}
