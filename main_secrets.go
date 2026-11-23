package main

import (
	"errors"
	"log"
	"sync"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
)

// The WebDAV password goes to the system's credential store through
// MyGo's Secrets module (the login Keychain on macOS, the Credential
// Manager on Windows). Package app only knows the SecretStore interface,
// so tests run without a keychain; where the system has no store the
// service falls back to an obfuscated 0600 file (app/backup.go).
//
// MyGo files the secrets under the app's identifier. Versions up to
// 0.1.9 shipped with the placeholder legacyIdentifier, so what they
// stored is moved to the current identifier the first time it is asked
// for (migratingSecrets).

// legacyIdentifier is the identifier of mygo.json up to version 0.1.9.
const legacyIdentifier = "com.example.lanflow"

// secretBackend is the part of mygo.SecretsModule the stores use.
type secretBackend interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// migratingSecrets reads and writes cur, and falls back to old (nil: no
// older store) for keys cur does not have, moving them over.
type migratingSecrets struct {
	cur secretBackend
	old func() secretBackend
}

// migrateMu keeps a move out of the old store apart from a Set or Delete
// of the same key: without it a Get that has read the old value could
// write it over a value stored meanwhile.
var migrateMu sync.Mutex

func (m migratingSecrets) legacy() secretBackend {
	if m.old == nil {
		return nil
	}
	return m.old()
}

func (m migratingSecrets) Get(key string) ([]byte, error) {
	v, err := m.cur.Get(key)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, err
	}
	old := m.legacy()
	if old == nil {
		return nil, app.ErrSecretNotFound
	}
	migrateMu.Lock()
	defer migrateMu.Unlock()
	if v, err := m.cur.Get(key); err == nil { // stored while this waited
		return v, nil
	}
	v, oerr := old.Get(key)
	if errors.Is(oerr, mygo.ErrSecretNotFound) || errors.Is(oerr, errors.ErrUnsupported) {
		return nil, app.ErrSecretNotFound
	}
	if oerr != nil {
		// The secret may well be there (the system asked the user about a
		// build with another signature, and was refused or timed out):
		// that is a failure to read, which the caller reports and tries
		// again, not a secret that was never stored.
		return nil, oerr
	}
	// The old copy goes only once the new one is in place, so that a
	// failure here costs nothing: the next Get migrates again.
	if serr := m.cur.Set(key, v); serr != nil {
		log.Printf("lanflow: move %q to the new credential store: %v", key, serr)
		return v, nil
	}
	if derr := old.Delete(key); derr != nil && !errors.Is(derr, mygo.ErrSecretNotFound) {
		log.Printf("lanflow: remove %q from the old credential store: %v", key, derr)
	}
	return v, nil
}

func (m migratingSecrets) Set(key string, value []byte) error {
	migrateMu.Lock()
	defer migrateMu.Unlock()
	if err := m.cur.Set(key, value); err != nil {
		return err
	}
	m.dropLegacy(key)
	return nil
}

// Delete removes the key from both stores: a copy left in the old one
// would come back at the next Get.
func (m migratingSecrets) Delete(key string) error {
	migrateMu.Lock()
	defer migrateMu.Unlock()
	err := m.cur.Delete(key)
	m.dropLegacy(key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return app.ErrSecretNotFound
	}
	return err
}

func (m migratingSecrets) dropLegacy(key string) {
	old := m.legacy()
	if old == nil {
		return
	}
	if err := old.Delete(key); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) && !errors.Is(err, errors.ErrUnsupported) {
		log.Printf("lanflow: remove %q from the old credential store: %v", key, err)
	}
}

// legacySecrets is the store of the versions with legacyIdentifier. Only
// a packaged app has one: development builds file their secrets under
// "<identifier>.dev" or the app's name, which no release ever used.
func legacySecrets() secretBackend {
	if !mygo.App.IsPackaged() {
		return nil
	}
	return mygo.Secrets.Service(legacyIdentifier)
}

func init() { app.Secrets = migratingSecrets{cur: mygo.Secrets, old: legacySecrets} }
