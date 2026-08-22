package jim

type Jake struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func NewJake(firstName string, lastName string) *Jake {
	return &Jake{FirstName: firstName, LastName: lastName}
}

func (j *Jake) String() string {
	if j == nil {
		return ""
	}
	return j.FirstName + " " + j.LastName
}

type Eeid struct {
	Name string
	Age  int
}
