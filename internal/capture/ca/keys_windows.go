package ca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The root's key on Windows is a persisted CNG key in the Microsoft
// Software Key Storage Provider with an export policy of 0: it cannot be
// exported, only used for signing by processes of this user
// (docs/capture.md §6). Untested on a real Windows machine (§15).

func systemKeys(SecretStore) KeyStore { return cngKeys{} }

var (
	ncrypt                  = windows.NewLazySystemDLL("ncrypt.dll")
	procOpenStorageProvider = ncrypt.NewProc("NCryptOpenStorageProvider")
	procCreatePersistedKey  = ncrypt.NewProc("NCryptCreatePersistedKey")
	procOpenKey             = ncrypt.NewProc("NCryptOpenKey")
	procSetProperty         = ncrypt.NewProc("NCryptSetProperty")
	procFinalizeKey         = ncrypt.NewProc("NCryptFinalizeKey")
	procExportKey           = ncrypt.NewProc("NCryptExportKey")
	procSignHash            = ncrypt.NewProc("NCryptSignHash")
	procDeleteKey           = ncrypt.NewProc("NCryptDeleteKey")
	procFreeObject          = ncrypt.NewProc("NCryptFreeObject")
)

const (
	ncryptOverwriteKeyFlag = 0x80
	ncryptPersistFlag      = 0x80000000
	nteBadKeyset           = 0x80090016
	eccPublicP256Magic     = 0x31534345 // BCRYPT_ECDSA_PUBLIC_P256_MAGIC
)

func u16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func status(r uintptr) error {
	if r == 0 {
		return nil
	}
	if uint32(r) == nteBadKeyset {
		return ErrNoKey
	}
	return fmt.Errorf("ncrypt: 0x%08x", uint32(r))
}

type cngKeys struct{}

func provider() (uintptr, error) {
	var p uintptr
	provName := u16("Microsoft Software Key Storage Provider")
	r, _, _ := procOpenStorageProvider.Call(uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(provName)), 0)
	runtime.KeepAlive(provName)
	return p, status(r)
}

func (cngKeys) Create(name string) (crypto.Signer, error) {
	prov, err := provider()
	if err != nil {
		return nil, err
	}
	var key uintptr
	alg := u16("ECDSA_P256")
	keyName := u16(name)
	r, _, _ := procCreatePersistedKey.Call(prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(alg)),
		uintptr(unsafe.Pointer(keyName)), 0, ncryptOverwriteKeyFlag)
	runtime.KeepAlive(alg)
	runtime.KeepAlive(keyName)
	if err := status(r); err != nil {
		procFreeObject.Call(prov)
		return nil, err
	}
	policy := uint32(0) // not exportable
	prop := u16("Export Policy")
	r, _, _ = procSetProperty.Call(key, uintptr(unsafe.Pointer(prop)), uintptr(unsafe.Pointer(&policy)), 4, ncryptPersistFlag)
	runtime.KeepAlive(prop)
	runtime.KeepAlive(&policy)
	if err := status(r); err != nil {
		procFreeObject.Call(key)
		procFreeObject.Call(prov)
		return nil, err
	}
	r, _, _ = procFinalizeKey.Call(key, 0)
	if err := status(r); err != nil {
		procFreeObject.Call(key)
		procFreeObject.Call(prov)
		return nil, err
	}
	return newCNGSigner(prov, key)
}

func (cngKeys) Open(name string) (crypto.Signer, error) {
	prov, err := provider()
	if err != nil {
		return nil, err
	}
	var key uintptr
	keyName := u16(name)
	r, _, _ := procOpenKey.Call(prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(keyName)), 0, 0)
	runtime.KeepAlive(keyName)
	if err := status(r); err != nil {
		procFreeObject.Call(prov)
		return nil, err
	}
	return newCNGSigner(prov, key)
}

func (cngKeys) Delete(name string) error {
	prov, err := provider()
	if err != nil {
		return err
	}
	defer procFreeObject.Call(prov)
	var key uintptr
	keyName := u16(name)
	r, _, _ := procOpenKey.Call(prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(keyName)), 0, 0)
	runtime.KeepAlive(keyName)
	if err := status(r); err != nil {
		if errors.Is(err, ErrNoKey) {
			return nil
		}
		return err
	}
	r, _, _ = procDeleteKey.Call(key, 0) // frees the handle only on success
	if err := status(r); err != nil {
		procFreeObject.Call(key)
		return err
	}
	return nil
}

type cngHandlePair struct {
	prov uintptr
	key  uintptr
}

func freeCNGPair(h cngHandlePair) {
	if h.key != 0 {
		procFreeObject.Call(h.key)
	}
	if h.prov != 0 {
		procFreeObject.Call(h.prov)
	}
}

type cngSigner struct {
	mu      sync.Mutex
	prov    uintptr
	key     uintptr
	pub     *ecdsa.PublicKey
	cleanup runtime.Cleanup
}

func (s *cngSigner) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup.Stop()
	freeCNGPair(cngHandlePair{prov: s.prov, key: s.key})
	s.prov, s.key = 0, 0
	return nil
}

// newCNGSigner reads the public key of key and takes ownership of prov and
// key: both are freed on error or when the signer is closed/collected.
func newCNGSigner(prov, key uintptr) (*cngSigner, error) {
	s, err := cngPublic(prov, key)
	if err != nil {
		freeCNGPair(cngHandlePair{prov: prov, key: key})
		return nil, err
	}
	s.cleanup = runtime.AddCleanup(s, freeCNGPair, cngHandlePair{prov: prov, key: key})
	return s, nil
}

func cngPublic(prov, key uintptr) (*cngSigner, error) {
	var n uint32
	blobType := u16("ECCPUBLICBLOB")
	r, _, _ := procExportKey.Call(key, 0, uintptr(unsafe.Pointer(blobType)), 0, 0, 0, uintptr(unsafe.Pointer(&n)), 0)
	runtime.KeepAlive(blobType)
	runtime.KeepAlive(&n)
	if err := status(r); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("ncrypt: empty public key blob")
	}
	buf := make([]byte, n)
	r, _, _ = procExportKey.Call(key, 0, uintptr(unsafe.Pointer(blobType)), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&n)), 0)
	runtime.KeepAlive(blobType)
	runtime.KeepAlive(buf)
	runtime.KeepAlive(&n)
	if err := status(r); err != nil {
		return nil, err
	}
	if len(buf) < 8 || binary.LittleEndian.Uint32(buf) != eccPublicP256Magic {
		return nil, errors.New("ncrypt: unexpected public key blob")
	}
	sz := int(binary.LittleEndian.Uint32(buf[4:]))
	if len(buf) < 8+2*sz {
		return nil, errors.New("ncrypt: short public key blob")
	}
	//lint:ignore SA1019 the CNG blob carries the raw coordinates; the key is only read.
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(buf[8 : 8+sz]), Y: new(big.Int).SetBytes(buf[8+sz : 8+2*sz])}
	return &cngSigner{prov: prov, key: key, pub: pub}, nil
}

func (s *cngSigner) Public() crypto.PublicKey { return s.pub }

// Sign signs a digest; CNG returns r||s, Go wants ASN.1. NCRYPT_KEY_HANDLE
// is not thread-safe, so calls are serialized under s.mu.
func (s *cngSigner) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	if len(digest) == 0 {
		return nil, errors.New("ncrypt: empty digest")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == 0 {
		return nil, errors.New("ncrypt: key closed")
	}
	var n uint32
	r, _, _ := procSignHash.Call(s.key, 0, uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), 0, 0, uintptr(unsafe.Pointer(&n)), 0)
	runtime.KeepAlive(digest)
	runtime.KeepAlive(&n)
	if err := status(r); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("ncrypt: empty signature")
	}
	sig := make([]byte, n)
	r, _, _ = procSignHash.Call(s.key, 0, uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), uintptr(unsafe.Pointer(&sig[0])), uintptr(n), uintptr(unsafe.Pointer(&n)), 0)
	runtime.KeepAlive(digest)
	runtime.KeepAlive(sig)
	runtime.KeepAlive(&n)
	if err := status(r); err != nil {
		return nil, err
	}
	sig = sig[:n]
	half := len(sig) / 2
	return asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(sig[:half]), new(big.Int).SetBytes(sig[half:])})
}
