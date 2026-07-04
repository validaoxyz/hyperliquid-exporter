package replica

import (
	"encoding/json"
	"testing"
)

func TestCountJSONArrayElements(t *testing.T) {
	order := `{"a":0,"b":"BTC","p":"50000","s":"0.1","r":false,"t":{"limit":{"tif":"Gtc"}}}`
	cases := []struct {
		name string
		data string
		want int
	}{
		{"empty array", `[]`, 0},
		{"one order object", `[` + order + `]`, 1},
		{"two order objects", `[` + order + `,` + order + `]`, 2},
		{"one cancel", `[{"a":1,"o":12345}]`, 1},
		{"nested arrays inside objects", `[{"x":[1,2,3]},{"y":[[4,5]]}]`, 2},
		{"strings with commas and braces", `[{"s":"a,b}{c"},{"s":"d"}]`, 2},
	}
	for _, c := range cases {
		if got := countJSONArrayElements(json.RawMessage(c.data)); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
