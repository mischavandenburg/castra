package pick

import (
	"strings"
	"testing"
)

func TestMenuKeys(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want outcome
		pick int
	}{
		{"enter picks the first", []string{"\r"}, chosen, 0},
		{"down arrow then enter", []string{"\x1b[B", "\r"}, chosen, 1},
		{"j j enter", []string{"j", "j", "\r"}, chosen, 2},
		{"down past the end stops at the last", []string{"j", "j", "j", "j", "\r"}, chosen, 2},
		{"up at the top stays", []string{"\x1b[A", "k", "\r"}, chosen, 0},
		{"application-mode arrows", []string{"\x1bOB", "\r"}, chosen, 1},
		{"digit jumps and picks", []string{"3"}, chosen, 2},
		{"digit past the list does nothing", []string{"7", "\r"}, chosen, 0},
		{"q cancels", []string{"j", "q"}, canceled, 1},
		{"Esc cancels", []string{"\x1b"}, canceled, 0},
		{"Ctrl-C cancels", []string{"\x03"}, canceled, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &menu{items: []string{"a", "b", "c"}}
			got := keepGoing
			for _, k := range c.keys {
				if got = m.key([]byte(k)); got != keepGoing {
					break
				}
			}
			if got != c.want || m.selected != c.pick {
				t.Fatalf("outcome %v selected %d, want %v selected %d", got, m.selected, c.want, c.pick)
			}
		})
	}
}

func TestBatchedInputIsSplitIntoKeys(t *testing.T) {
	m := &menu{items: []string{"a", "b", "c"}}
	got := keepGoing
	for _, k := range splitKeys([]byte("\x1b[Bj\r")) { // down arrow, j, Enter in one read
		if got = m.key(k); got != keepGoing {
			break
		}
	}
	if got != chosen || m.selected != 2 {
		t.Fatalf("outcome %v selected %d, want chosen 2", got, m.selected)
	}
	if keys := splitKeys([]byte("\x1b")); len(keys) != 1 || string(keys[0]) != "\x1b" {
		t.Fatalf("a lone Esc split into %q", keys)
	}
}

func TestRenderMarksSelection(t *testing.T) {
	m := &menu{items: []string{"alpha", "beta"}, selected: 1}
	out := m.render("Pick one")
	if !strings.HasPrefix(out, "Pick one\r\n") || !strings.Contains(out, "> 2  beta") || !strings.Contains(out, "  1  alpha") {
		t.Fatalf("render:\n%q", out)
	}
}
