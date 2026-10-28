// Package pick is a small terminal menu: arrow keys or j/k to move, Enter to
// choose, a digit to jump, q, Esc or Ctrl-C to cancel.
package pick

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ErrCanceled means the user left the menu without choosing.
var ErrCanceled = errors.New("canceled")

// menu is the menu's state, kept apart from the terminal so it can be tested.
type menu struct {
	items    []string
	selected int
}

type outcome int

const (
	keepGoing outcome = iota
	chosen
	canceled
)

// key applies one keypress (as read from a raw terminal) to the menu.
func (m *menu) key(k []byte) outcome {
	switch s := string(k); {
	case s == "\r" || s == "\n":
		return chosen
	case s == "q" || s == "\x1b" || s == "\x03": // q, Esc, Ctrl-C
		return canceled
	case s == "\x1b[A" || s == "\x1bOA" || s == "k":
		if m.selected > 0 {
			m.selected--
		}
	case s == "\x1b[B" || s == "\x1bOB" || s == "j":
		if m.selected < len(m.items)-1 {
			m.selected++
		}
	case len(s) == 1 && s[0] >= '1' && s[0] <= '9':
		if n := int(s[0] - '1'); n < len(m.items) {
			m.selected = n
			return chosen
		}
	}
	return keepGoing
}

// splitKeys cuts one read from the terminal into keypresses: fast typing or a
// paste can deliver several keys at once, and an arrow key is three bytes.
func splitKeys(b []byte) [][]byte {
	var keys [][]byte
	for len(b) > 0 {
		n := 1
		if b[0] == 0x1b && len(b) >= 3 && (b[1] == '[' || b[1] == 'O') {
			n = 3
		}
		keys = append(keys, b[:n])
		b = b[n:]
	}
	return keys
}

// render draws the menu; lines end in \r\n because the terminal is raw.
func (m *menu) render(title string) string {
	var b strings.Builder
	b.WriteString(title + "\r\n")
	for i, item := range m.items {
		if i == m.selected {
			fmt.Fprintf(&b, "\x1b[1m> %d  %s\x1b[0m\x1b[K\r\n", i+1, item)
		} else {
			fmt.Fprintf(&b, "  %d  %s\x1b[K\r\n", i+1, item)
		}
	}
	return b.String()
}

// Pick shows the menu on the terminal and returns the index chosen.
func Pick(in *os.File, out io.Writer, title string, items []string) (int, error) {
	if len(items) == 0 {
		return 0, errors.New("nothing to choose from")
	}
	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		return 0, errors.New("not a terminal, so there is no menu to show")
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return 0, err
	}
	defer func() { _ = term.Restore(fd, state) }()

	m := &menu{items: items}
	lines := len(items) + 1
	fmt.Fprint(out, "\x1b[?25l") // hide the cursor while the menu is up
	defer fmt.Fprint(out, "\x1b[?25h")
	fmt.Fprint(out, m.render(title))

	buf := make([]byte, 64)
	for {
		n, err := in.Read(buf)
		if err != nil {
			return 0, err
		}
		result := keepGoing
		for _, k := range splitKeys(buf[:n]) {
			if result = m.key(k); result != keepGoing {
				break
			}
		}
		fmt.Fprintf(out, "\x1b[%dA", lines) // back to the title line
		if result != keepGoing {
			fmt.Fprint(out, "\x1b[J") // erase the menu
			if result == canceled {
				return 0, ErrCanceled
			}
			return m.selected, nil
		}
		fmt.Fprint(out, m.render(title))
	}
}
