package main

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
)

func TestIdentifierMatchesManifest(t *testing.T) {
	data, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Identifier != appIdentifier {
		t.Fatalf("mygo.json identifier = %q, appIdentifier = %q", m.Identifier, appIdentifier)
	}
	if m.Identifier == legacyIdentifier {
		t.Fatalf("identifier is still the placeholder %q", legacyIdentifier)
	}
}

func TestSameInstallDir(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{`"C:\Users\me\AppData\Local\Programs\Lanflow"`, `C:\Users\me\AppData\Local\Programs\Lanflow`, true},
		{`C:\Apps\Lanflow`, `"c:\apps\lanflow"`, true},
		{`C:\Apps\Lanflow`, `D:\Apps\Lanflow`, false},
		{``, ``, false},
		{``, `C:\Apps\Lanflow`, false},
	} {
		if got := sameInstallDir(c.a, c.b); got != c.want {
			t.Errorf("sameInstallDir(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// memSecrets is a credential store in memory; fail makes every call of
// the named operation fail.
type memSecrets struct {
	m    map[string][]byte
	fail map[string]error
}

func newMemSecrets(kv ...string) *memSecrets {
	s := &memSecrets{m: map[string][]byte{}, fail: map[string]error{}}
	for i := 0; i+1 < len(kv); i += 2 {
		s.m[kv[i]] = []byte(kv[i+1])
	}
	return s
}

func (s *memSecrets) Get(key string) ([]byte, error) {
	if err := s.fail["get"]; err != nil {
		return nil, err
	}
	v, ok := s.m[key]
	if !ok {
		return nil, mygo.ErrSecretNotFound
	}
	return v, nil
}

func (s *memSecrets) Set(key string, value []byte) error {
	if err := s.fail["set"]; err != nil {
		return err
	}
	s.m[key] = value
	return nil
}

func (s *memSecrets) Delete(key string) error {
	if err := s.fail["delete"]; err != nil {
		return err
	}
	if _, ok := s.m[key]; !ok {
		return mygo.ErrSecretNotFound
	}
	delete(s.m, key)
	return nil
}

func migrating(cur, old *memSecrets) migratingSecrets {
	m := migratingSecrets{cur: cur}
	if old != nil {
		m.old = func() secretBackend { return old }
	}
	return m
}

func TestSecretsMigrateFromLegacyIdentifier(t *testing.T) {
	cur, old := newMemSecrets(), newMemSecrets("webdav", "hunter2")
	m := migrating(cur, old)
	v, err := m.Get("webdav")
	if err != nil || string(v) != "hunter2" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if string(cur.m["webdav"]) != "hunter2" {
		t.Fatalf("the secret was not moved to the current store: %v", cur.m)
	}
	if _, ok := old.m["webdav"]; ok {
		t.Fatalf("the secret is still in the old store")
	}
}

func TestSecretsCurrentStoreWins(t *testing.T) {
	cur, old := newMemSecrets("webdav", "new"), newMemSecrets("webdav", "old")
	v, err := migrating(cur, old).Get("webdav")
	if err != nil || string(v) != "new" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if string(old.m["webdav"]) != "old" {
		t.Fatalf("a plain read must not touch the old store")
	}
}

func TestSecretsMissingEverywhere(t *testing.T) {
	for name, m := range map[string]migratingSecrets{
		"with legacy":    migrating(newMemSecrets(), newMemSecrets()),
		"without legacy": migrating(newMemSecrets(), nil),
	} {
		if _, err := m.Get("webdav"); !errors.Is(err, app.ErrSecretNotFound) {
			t.Errorf("%s: Get = %v, want app.ErrSecretNotFound", name, err)
		}
	}
}

// A current store that fails for another reason is reported as is: the
// old store must not paper over it.
func TestSecretsCurrentStoreErrorIsNotMigrated(t *testing.T) {
	boom := errors.New("keychain locked")
	cur, old := newMemSecrets(), newMemSecrets("webdav", "old")
	cur.fail["get"] = boom
	if _, err := migrating(cur, old).Get("webdav"); !errors.Is(err, boom) {
		t.Fatalf("Get = %v, want %v", err, boom)
	}
}

// The old store may refuse a build with another signature: that is a
// failure to read, reported as one, not a secret that was never stored
// (the caller would then let the user overwrite it).
func TestSecretsUnreadableLegacyIsAnError(t *testing.T) {
	denied := errors.New("user denied")
	cur, old := newMemSecrets(), newMemSecrets("webdav", "old")
	old.fail["get"] = denied
	if _, err := migrating(cur, old).Get("webdav"); !errors.Is(err, denied) {
		t.Fatalf("Get = %v, want %v", err, denied)
	}
	old.fail["get"] = errors.ErrUnsupported // no credential store at all
	if _, err := migrating(cur, old).Get("webdav"); !errors.Is(err, app.ErrSecretNotFound) {
		t.Fatalf("Get = %v, want app.ErrSecretNotFound", err)
	}
}

// When the move fails the value is still returned and the old copy kept,
// so the next Get tries again.
func TestSecretsFailedMoveKeepsLegacy(t *testing.T) {
	cur, old := newMemSecrets(), newMemSecrets("webdav", "old")
	cur.fail["set"] = errors.New("read-only")
	v, err := migrating(cur, old).Get("webdav")
	if err != nil || string(v) != "old" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if string(old.m["webdav"]) != "old" {
		t.Fatalf("the old copy was removed although the move failed")
	}
}

func TestSecretsSetAndDeleteDropLegacy(t *testing.T) {
	cur, old := newMemSecrets(), newMemSecrets("webdav", "old")
	m := migrating(cur, old)
	if err := m.Set("webdav", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, ok := old.m["webdav"]; ok {
		t.Fatalf("Set left the old copy, which a later Delete+Get would bring back")
	}

	cur, old = newMemSecrets(), newMemSecrets("webdav", "old")
	m = migrating(cur, old)
	if err := m.Delete("webdav"); !errors.Is(err, app.ErrSecretNotFound) {
		t.Fatalf("Delete = %v, want app.ErrSecretNotFound from the current store", err)
	}
	if _, err := m.Get("webdav"); !errors.Is(err, app.ErrSecretNotFound) {
		t.Fatalf("the deleted secret came back from the old store: %v", err)
	}
}
