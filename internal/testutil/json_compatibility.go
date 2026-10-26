// Package testutil provides generated inputs for parser compatibility tests.
package testutil

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"strings"
)

type JSONCompatibilityCase struct {
	Name string
	Raw  []byte
}

// JSONCompatibilityCases includes valid JSON and deliberately invalid bytes.
// Its fixed seed and fingerprint let separate parser tests bind the same corpus.
func JSONCompatibilityCases() []JSONCompatibilityCase {
	var cases []JSONCompatibilityCase
	add := func(name string, raw []byte) {
		cases = append(cases, JSONCompatibilityCase{Name: name, Raw: append([]byte(nil), raw...)})
	}
	base := []string{
		``, `null`, `true`, `false`, `0`, `-0`, `1.5`, `1e99999`, `01`, `+1`, `NaN`,
		`""`, `"success"`, `"su\u0063cess"`, `"future"`, `"null"`, `"\ud800"`, `"\ufffd"`,
		`{}`, `[]`, `[null]`, `{"resting":{}}`, `{"error":"constructed"}`, `{"waitingForTrigger":null}`,
		`{"future":[1,{"k":true}]}`, `{"resting":1,"error":2}`, `{"resting":null}`, `{"resting":1e99999}`,
		`{"resting":1,"resting":null}`, `{"resting":1,"\u0072esting":2}`,
		`{"\ud800":1,"\ufffd":2}`, `{"k":"commas, braces{} and brackets[]"}`,
		`{"k":"quote\" slash\\ line\n"}`, `{"k":[null,false,{},[1,2]]}`,
		`{"k":}`, `{"k":1,}`, `{"k" 1}`, `{"k":01}`, `{"k":NaN}`, `{"k":"\x"}`,
		`{"k":"\u00zz"}`, `{"k":"unterminated}`, `{"k":1`, `{`, `}`, `[`, `]`,
		`{"k":1}null`, `{"k":1}{}`, `"success"null`, `"success" "future"`,
		`{"k":1/*comment*/}`, `/*comment*/{}`, `{"k":1}//comment`,
		string([]byte{'"', 0xff, '"'}), string([]byte{'{', '"', 0xff, '"', ':', '1', '}'}),
		string([]byte{'{', '"', 0xc0, 0xaf, '"', ':', '1', '}'}),
		string([]byte{'{', '"', 'k', '"', ':', '"', 0xed, 0xa0, 0x80, '"', '}'}),
		string([]byte{'{', '"', 'k', '"', ':', '"', 0, '"', '}'}),
		string([]byte{0xef, 0xbb, 0xbf, '{', '}'}),
	}
	for i, raw := range base {
		add(fmt.Sprintf("base/%d", i), []byte(raw))
	}
	spaces := []string{" ", "\t", "\r\n", " \t\r\n ", "\v", "\f", "\u0085", "\u00a0", "\u2003", "\u2028", "\u202f", "\u3000", "\u200b", "\x00", "\xff"}
	for i, space := range spaces {
		for j, raw := range base {
			add(fmt.Sprintf("space-prefix/%d/%d", i, j), []byte(space+raw))
			add(fmt.Sprintf("space-suffix/%d/%d", i, j), []byte(raw+space))
			add(fmt.Sprintf("space-both/%d/%d", i, j), []byte(space+raw+space))
		}
	}
	keys := []string{`"resting"`, `"\u0072esting"`, `"error"`, `"waitingForTrigger"`, `"future"`, `""`, `"\ud800"`, `"\ufffd"`, string([]byte{'"', 0xff, '"'}), string([]byte{'"', 0xfe, '"'})}
	values := []string{`null`, `true`, `false`, `0`, `1e99999`, `""`, `"success"`, `"\ud800"`, `[]`, `[{},null]`, `{}`, `{"nested":[1,2]}`}
	for i, key := range keys {
		for j, value := range values {
			add(fmt.Sprintf("key-value/%d/%d", i, j), []byte("{"+key+":"+value+"}"))
		}
		for j, other := range keys {
			for k, value := range values {
				add(fmt.Sprintf("duplicate-keys/%d/%d/%d", i, j, k), []byte("{"+key+":"+value+","+other+":{}}"))
			}
		}
	}
	for _, depth := range []int{1, 2, 32, 255, 9999, 10000, 10001} {
		arrays := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		add(fmt.Sprintf("deep-array/%d", depth), []byte(arrays))
		add(fmt.Sprintf("deep-object-array/%d", depth), []byte(`{"resting":`+arrays+`}`))
		add(fmt.Sprintf("deep-objects/%d", depth), []byte(strings.Repeat(`{"resting":`, depth)+"0"+strings.Repeat("}", depth)))
		add(fmt.Sprintf("deep-incomplete/%d", depth), []byte(`{"resting":`+arrays))
	}
	rng := rand.New(rand.NewSource(20261026))
	var value func(int) string
	value = func(depth int) string {
		if depth == 0 || rng.Intn(3) == 0 {
			return values[rng.Intn(len(values))]
		}
		parts := make([]string, rng.Intn(4)+1)
		object := rng.Intn(2) == 0
		for i := range parts {
			parts[i] = value(depth - 1)
			if object {
				parts[i] = keys[rng.Intn(len(keys))] + ":" + parts[i]
			}
		}
		if object {
			return "{" + strings.Join(parts, ",") + "}"
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	for i := 0; i < 1024; i++ {
		add(fmt.Sprintf("generated/%d", i), []byte(value(4)))
	}
	for i, raw := range base {
		for cut := 0; cut < len(raw); cut++ {
			add(fmt.Sprintf("truncated/%d/%d", i, cut), []byte(raw[:cut]))
		}
		if len(raw) == 0 {
			continue
		}
		for _, pos := range []int{0, len(raw) / 2, len(raw) - 1} {
			for _, replacement := range []byte{0, 0xff, '"', '{', '}', '[', ']', ',', ':', '\v'} {
				mutated := []byte(raw)
				mutated[pos] = replacement
				add(fmt.Sprintf("mutated/%d/%d/%d", i, pos, replacement), mutated)
			}
		}
	}
	for i := 0; i < 2048; i++ {
		raw := make([]byte, rng.Intn(129))
		_, _ = rng.Read(raw)
		add(fmt.Sprintf("arbitrary-bytes/%d", i), raw)
	}
	return cases
}

func JSONCompatibilityFingerprint(cases []JSONCompatibilityCase) string {
	hash := sha256.New()
	for _, item := range cases {
		_, _ = fmt.Fprintf(hash, "%d:%s%d:", len(item.Name), item.Name, len(item.Raw))
		_, _ = hash.Write(item.Raw)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
