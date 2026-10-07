package networkjson_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"goodkind.io/mwan/internal/networkjson"
)

func requireCanonical(t *testing.T, input string, want string) {
	t.Helper()
	got, err := networkjson.Canonicalize([]byte(input))
	if err != nil {
		t.Fatalf("Canonicalize(%q): %v", input, err)
	}
	if string(got) != want {
		t.Fatalf("Canonicalize(%q) = %s, want %s", input, got, want)
	}
}

func jsonEscape(unit uint16) string {
	return fmt.Sprintf(`\`+"u%04x", unit)
}

func TestCanonicalizeIgnoresWhitespaceAndMemberOrder(t *testing.T) {
	want := `{"a":"value","b":[1,{"x":null,"y":true}],"c":{}}`
	for _, input := range []string{
		`{"b":[1,{"y":true,"x":null}],"a":"value","c":{}}`,
		"\n{ \"c\" : { } ,\n\t\"a\": \"value\",\r\n  \"b\": [ 1 , { \"x\": null, \"y\": true } ] }\n",
	} {
		requireCanonical(t, input, want)
	}
}

func TestCanonicalizeSortsMembersByDecodedName(t *testing.T) {
	requireCanonical(t, `{"b":1,"`+jsonEscape('a')+`":2,"B":3}`, `{"B":3,"a":2,"b":1}`)
}

func TestCanonicalizePreservesArrayOrder(t *testing.T) {
	requireCanonical(t, `[3, 1, 2, "b", "a", [{"z":1}, {"a":1}]]`, `[3,1,2,"b","a",[{"z":1},{"a":1}]]`)
}

func TestCanonicalizePreservesNumberLiterals(t *testing.T) {
	requireCanonical(t,
		`{"n": [1.0, 1e2, 0, -0, 1E+2, 0.10, 18446744073709551616]}`,
		`{"n":[1.0,1e2,0,-0,1E+2,0.10,18446744073709551616]}`)
}

func TestCanonicalizePreservesStringValues(t *testing.T) {
	escapedHTML := jsonEscape('<') + jsonEscape('&') + jsonEscape('>')
	lineSeparator := jsonEscape(0x2028)
	surrogatePair := jsonEscape(0xd83d) + jsonEscape(0xde00)
	input := `{"html":"<a href=\"x\">&amp;</a>","escaped":"` + escapedHTML + `",` +
		`"text":"café ☃ ` + lineSeparator + ` tab\t ` + surrogatePair + ` \\ \/"}`
	got, err := networkjson.Canonicalize([]byte(input))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	var original map[string]string
	if err := json.Unmarshal([]byte(input), &original); err != nil {
		t.Fatalf("decode input: %v", err)
	}
	var canonical map[string]string
	if err := json.Unmarshal(got, &canonical); err != nil {
		t.Fatalf("decode output %s: %v", got, err)
	}
	if !reflect.DeepEqual(canonical, original) {
		t.Fatalf("string values differ:\ngot  %q\nwant %q", canonical, original)
	}
}

func requireCanonicalizeError(t *testing.T, input string, want string) {
	t.Helper()
	got, err := networkjson.Canonicalize([]byte(input))
	if err == nil || err.Error() != want {
		t.Fatalf("Canonicalize(%q) = %s, %v; want error %q", input, got, err, want)
	}
}

func TestCanonicalizeRejectsDuplicateMembers(t *testing.T) {
	for _, testCase := range []struct {
		input  string
		member string
	}{
		{input: `{"a":1,"a":1}`, member: "a"},
		{input: `{"outer":{"k":true,"k":false}}`, member: "k"},
		{input: `{"a":1,"` + jsonEscape('a') + `":2}`, member: "a"},
		{input: `[{"name":"x","name":"y"}]`, member: "name"},
	} {
		requireCanonicalizeError(t, testCase.input,
			`canonicalize: object member "`+testCase.member+`" appears more than once`)
	}
	requireCanonical(t, `[{"a":1},{"a":2}]`, `[{"a":1},{"a":2}]`)
}

func TestCanonicalizeRejectsInvalidDocuments(t *testing.T) {
	const (
		truncated  = "canonicalize: unexpected EOF"
		endOfInput = "canonicalize: unexpected end of JSON input"
		trailing   = "canonicalize: trailing data after the top-level value"
	)
	for _, testCase := range []struct {
		input string
		want  string
	}{
		{input: ``, want: truncated},
		{input: `   `, want: truncated},
		{input: `tru`, want: truncated},
		{input: `{`, want: endOfInput},
		{input: `{"a":"x"`, want: endOfInput},
		{input: `["x"`, want: endOfInput},
		{input: `[1`, want: endOfInput},
		{input: `{} {}`, want: trailing},
		{input: `{}x`, want: trailing},
		{input: `{"a":1}}`, want: trailing},
		{input: `[1,2]]`, want: trailing},
		{input: `{"a":}`, want: "canonicalize: missing value after object key"},
		{input: `{"a" 1}`, want: "canonicalize: invalid character '1' after object key"},
		{input: `[1,]`, want: "canonicalize: invalid character ',' looking for beginning of value"},
		{input: `{1:2}`, want: "canonicalize: object member name must be a string"},
		{input: "\"\xff\"", want: "canonicalize: document is not valid UTF-8"},
	} {
		requireCanonicalizeError(t, testCase.input, testCase.want)
	}
}
