package shell

import (
	"os/exec"
	"testing"
)

func TestQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "''"},
		{"plain", "plain"},
		{"/usr/local/bin/castra", "/usr/local/bin/castra"},
		{"a@b%c+d=e:f,g.h/i_j-k", "a@b%c+d=e:f,g.h/i_j-k"},
		{"with space", "'with space'"},
		{"it's", `'it'\''s'`},
		{"$HOME", "'$HOME'"},
		{"`id`", "'`id`'"},
		{"a;rm -rf /", "'a;rm -rf /'"},
		{"two\nlines", "'two\nlines'"},
		{"tab\there", "'tab\there'"},
		{"ümlaut", "'ümlaut'"},
	}
	for _, c := range cases {
		if got := Quote(c.in); got != c.want {
			t.Errorf("Quote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJoin(t *testing.T) {
	if got := Join([]string{"git", "commit", "-m", "it's done"}); got != `git commit -m 'it'\''s done'` {
		t.Fatalf("Join = %q", got)
	}
	if got := Join(nil); got != "" {
		t.Fatalf("Join(nil) = %q", got)
	}
}

// TestShRoundTrip is the test that matters: sh must read every quoted word
// back as the original string, byte for byte.
func TestShRoundTrip(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	for _, s := range []string{
		"", "plain", "with space", "it's", "$HOME", "`id`", "a;rm -rf /",
		"two\nlines", `back\slash`, `"double"`, "'single'", "*glob?", "~tilde", "#hash", "a&&b||c", "<redirect>",
	} {
		out, err := exec.Command(sh, "-c", "printf %s "+Quote(s)).Output()
		if err != nil {
			t.Fatalf("sh rejected %q: %v", Quote(s), err)
		}
		if string(out) != s {
			t.Errorf("sh read %q back as %q", s, out)
		}
	}
}
