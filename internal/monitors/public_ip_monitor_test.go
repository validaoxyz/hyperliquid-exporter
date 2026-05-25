package monitors

import "testing"

func TestParsePublicIP(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
		ok   bool
	}{
		{"json-string", `"160.202.131.51"`, "160.202.131.51", true},
		{"json-string with trailing newline", "\"1.2.3.4\"\n", "1.2.3.4", true},
		{"bare string", "1.2.3.4", "1.2.3.4", true},
		{"quoted bare", "'5.6.7.8'", "5.6.7.8", true},
		{"empty", "", "", false},
		{"whitespace only", "   \n", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parsePublicIP([]byte(c.data))
			if ok != c.ok {
				t.Fatalf("ok=%v want %v (data %q)", ok, c.ok, c.data)
			}
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}
