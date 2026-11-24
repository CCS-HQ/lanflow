//go:build windows

package ca

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCNGKeysLifecycleAndConcurrency(t *testing.T) {
	ks := cngKeys{}
	name := fmt.Sprintf("lanflow-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = ks.Delete(name) })

	s1, err := ks.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := s1.(interface{ Close() error }); ok {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s2, err := ks.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if c, ok := s2.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}()
	pub, ok := s2.Public().(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("unexpected public key %T", s2.Public())
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sum := sha256.Sum256([]byte(fmt.Sprintf("msg-%d", idx)))
			sig, err := s2.Sign(rand.Reader, sum[:], nil)
			if err != nil {
				errs <- err
				return
			}
			if !ecdsa.VerifyASN1(pub, sum[:], sig) {
				errs <- fmt.Errorf("invalid signature for %d", idx)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if err := ks.Delete(name); err != nil {
		t.Fatal(err)
	}
	if _, err := ks.Open(name); err == nil {
		t.Fatal("deleted key still opened")
	}
}
