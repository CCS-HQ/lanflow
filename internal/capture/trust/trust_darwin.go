//go:build !ios

package trust

import (
	"bufio"
	"bytes"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lanflow/internal/capture/ca"
)

func loginKeychain() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db")
}

func withPEM(der []byte, fn func(path string) error) error {
	f, err := os.CreateTemp("", "lanflow-ca-*.pem")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.Write(ca.EncodePEM(der))
	f.Close()
	return fn(f.Name())
}

// run runs a command; tests replace it (never the real trust store).
var run = func(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func install(der []byte) error {
	return withPEM(der, func(p string) error {
		// user domain: macOS shows its own authorization dialog
		return run("/usr/bin/security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", loginKeychain(), p)
	})
}

func remove(der []byte) error {
	err := withPEM(der, func(p string) error {
		return run("/usr/bin/security", "remove-trusted-cert", p)
	})
	derr := run("/usr/bin/security", "delete-certificate", "-Z", SHA1(der), loginKeychain())
	if err != nil && derr != nil {
		if strings.Contains(derr.Error(), "could not be found") || strings.Contains(derr.Error(), "Unable to delete") {
			return nil
		}
		return err
	}
	return nil
}

// findCerts returns DER bytes for Lanflow roots in the login keychain; tests
// replace it.
var findCerts = func() [][]byte {
	out, err := exec.Command("/usr/bin/security", "find-certificate", "-a", "-c", "Lanflow Local CA", "-p", loginKeychain()).Output()
	if err != nil {
		return nil
	}
	var ders [][]byte
	for len(out) > 0 {
		var blk *pem.Block
		blk, out = pem.Decode(out)
		if blk == nil {
			break
		}
		if blk.Type == "CERTIFICATE" && len(blk.Bytes) > 0 {
			ders = append(ders, blk.Bytes)
		}
	}
	return ders
}

func removeByHash(hash string) error {
	want := strings.ToUpper(strings.TrimSpace(hash))
	if want == "" {
		return nil
	}
	for _, der := range findCerts() {
		if strings.EqualFold(SHA1(der), want) {
			return remove(der)
		}
	}
	derr := run("/usr/bin/security", "delete-certificate", "-Z", want, loginKeychain())
	if derr != nil {
		if strings.Contains(derr.Error(), "could not be found") || strings.Contains(derr.Error(), "Unable to delete") {
			return nil
		}
		return derr
	}
	return nil
}

func find() []string {
	out, err := exec.Command("/usr/bin/security", "find-certificate", "-a", "-c", "Lanflow Local CA", "-Z", loginKeychain()).Output()
	if err != nil {
		return nil
	}
	var hs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if h, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "SHA-1 hash: "); ok {
			hs = append(hs, strings.ToUpper(h))
		}
	}
	return hs
}
