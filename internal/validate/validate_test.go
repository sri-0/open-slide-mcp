package validate

import "testing"

func TestParseError(t *testing.T) {
	body := "<html><script>\n const error = {\"message\":\"Transform failed with 1 error:\\n\\n[PARSE_ERROR] Unterminated regular expression\\n   ╭─[ slides/broken/index.tsx:2:28 ]\\n\",\"stack\":\"x\"}\n              try {</script>"
	r := ParseError(500, body)
	if r.OK || r.Line != 2 || r.Column != 28 {
		t.Fatalf("got %+v", r)
	}
	if r2 := ParseError(500, "garbage"); r2.OK || r2.Message == "" {
		t.Fatalf("got %+v", r2)
	}
}
