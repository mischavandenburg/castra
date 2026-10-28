// Package shell quotes strings for POSIX sh. castra builds small scripts for
// `sh -c` inside the container and command lines for OpenSSH; every value
// that is not a fixed literal goes through Quote first.
package shell

import "strings"

// Quote returns s as one sh word. Strings made only of letters, digits and
// the characters @%+=:,./_- are returned as they are, since no shell treats
// them specially; everything else is single-quoted, with embedded single
// quotes closed, escaped and reopened. The empty string becomes a pair of
// single quotes.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes every argument and joins them with spaces, which is how a
// command and its arguments are handed to a remote shell.
func Join(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = Quote(a)
	}
	return strings.Join(quoted, " ")
}

func safe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("@%+=:,./_-", r)
}
