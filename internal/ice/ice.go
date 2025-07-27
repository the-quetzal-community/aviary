package ice

import "encoding/json"

type Server struct {
	Credential string `json:"credential"`
	URLs       URLs   `json:"urls,omitempty"`
	Username   string `json:"username"`
}

type URLs []string

func (u URLs) MarshalJSON() ([]byte, error) {
	if len(u) == 1 {
		return json.Marshal(u[0])
	}
	return json.Marshal(([]string)(u))
}

func (u *URLs) UnmarshalJSON(data []byte) error {
	if data[0] == '"' && data[len(data)-1] == '"' {
		var single string
		if err := json.Unmarshal(data, &single); err != nil {
			return err
		}
		*u = URLs{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return err
	}
	*u = URLs(multiple)
	return nil
}
