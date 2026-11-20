package trust

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// remove takes the trust settings away and deletes the certificate; it
// fails only when neither worked and the certificate is still there. The
// commands are replaced: the real trust store is never touched.
func TestRemoveMergesTheTwoCommands(t *testing.T) {
	denied := errors.New("security remove-trusted-cert: exit status 1: The authorization was denied")
	gone := errors.New("security delete-certificate: exit status 1: The specified item could not be found in the keychain")
	unable := errors.New("security delete-certificate: exit status 1: Unable to delete certificate matching \"AB\"")
	busy := errors.New("security delete-certificate: exit status 1: keychain is locked")
	cases := []struct {
		name         string
		untrust, del error
		wantErr      error
	}{
		{"both work", nil, nil, nil},
		{"certificate was never installed", denied, gone, nil},
		{"nothing to delete", denied, unable, nil},
		{"trust removed, certificate stays", nil, busy, nil},
		{"trust settings refused, certificate deleted", denied, nil, nil},
		{"both refused: still trusted", denied, busy, denied},
	}
	old := run
	defer func() { run = old }()
	der := []byte("not a real certificate")
	for _, c := range cases {
		var calls []string
		run = func(name string, args ...string) error {
			if name != "/usr/bin/security" {
				t.Fatalf("%s: ran %s", c.name, name)
			}
			calls = append(calls, args[0])
			switch args[0] {
			case "remove-trusted-cert":
				return c.untrust
			case "delete-certificate":
				if !slices.Contains(args, SHA1(der)) {
					t.Errorf("%s: delete-certificate without the hash: %v", c.name, args)
				}
				return c.del
			}
			t.Fatalf("%s: unexpected security %s", c.name, args[0])
			return nil
		}
		if err := remove(der); err != c.wantErr {
			t.Errorf("%s: err %v, want %v", c.name, err, c.wantErr)
		}
		if got := strings.Join(calls, ","); got != "remove-trusted-cert,delete-certificate" {
			t.Errorf("%s: ran %s", c.name, got)
		}
	}
}
