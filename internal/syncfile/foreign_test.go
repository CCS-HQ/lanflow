package syncfile

import (
	"strings"
	"testing"

	"lanflow/internal/model"
)

// TestForeign: files this build must never rewrite are told apart from
// files that are merely invalid, and from its own.
func TestForeign(t *testing.T) {
	t.Parallel()
	enc, key, err := NewEncryption("a long passphrase")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(key, SubscriptionPath("s1"), "https://a.example/sub")
	if err != nil {
		t.Fatal(err)
	}
	own := string(Encode(Doc{Settings: model.Settings{MixedPort: 7890}, Subscriptions: []Subscription{{ID: "s1", Name: "a", URL: SubscriptionRef("s1")}}}))
	ownSealed := string(Encode(Doc{Encryption: &enc, Subscriptions: []Subscription{{ID: "s1", Name: "a", URL: sealed}}}))
	for _, c := range []struct {
		name string
		data string
		want bool
	}{
		{"own file", own, false},
		{"own sealed file", ownSealed, false},
		{"empty", "", false},
		{"not yaml", "{{{", false},
		{"invalid setting", strings.Replace(own, "mixedPort: 7890", "mixedPort: lots", 1), false},
		{"no format", "settings: {}\n", false},
		{"newer format", strings.Replace(own, "format: 1", "format: 2", 1), true},
		{"much newer format", strings.Replace(own, "format: 1", "format: 17", 1), true},
		{"another kdf", strings.Replace(ownSealed, "kdf: "+KDFArgon2id, "kdf: scrypt", 1), true},
		{"encryption without kdf", "format: 1\nencryption:\n  salt: AAAA\n", true},
		{"encryption of another shape", "format: 1\nencryption: v2\n", true},
		{"sealed link of another version", strings.Replace(ownSealed, "url: "+sealed, "url: enc:v2:AAAA", 1), true},
		{"sealed password of another version", own + "webdavPassword: enc:v9:AAAA\n", true},
		{"check value of another version", strings.Replace(ownSealed, "check: "+enc.Check, "check: enc:v2:AAAA", 1), true},
	} {
		if got := Foreign([]byte(c.data)); got != c.want {
			t.Errorf("%s: Foreign = %v, want %v\n%s", c.name, got, c.want, c.data)
		}
	}
	// a newer format is refused by Decode as before
	if _, err := Decode([]byte(strings.Replace(own, "format: 1", "format: 2", 1)), model.Settings{}); err == nil {
		t.Error("a newer format decoded")
	}
}
