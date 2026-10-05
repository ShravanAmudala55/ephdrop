package identity

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// KeyFileName is the name of the private key file inside the data directory.
const KeyFileName = "identity.key"

const pemType = "PRIVATE KEY"

// LoadOrCreate loads the identity stored in dir, creating and saving a new one
// if none exists yet. The directory is created if needed.
//
// It is not safe to call concurrently from two processes on an empty
// directory. Each device runs a single ephdrop process, so this is fine.
func LoadOrCreate(dir string) (*Identity, error) {
	path := filepath.Join(dir, KeyFileName)

	id, err := Load(path)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("identity: create data dir: %w", err)
	}
	id, err = Generate()
	if err != nil {
		return nil, err
	}
	if err := id.Save(path); err != nil {
		return nil, err
	}
	return id, nil
}

// Load reads an identity from a PEM (PKCS#8) file.
// If the file does not exist the returned error satisfies
// errors.Is(err, fs.ErrNotExist).
func Load(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity: read key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != pemType {
		return nil, fmt.Errorf("%w: %s is not a PEM private key", ErrBadKey, path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadKey, err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: not an ed25519 key", ErrBadKey)
	}
	return &Identity{priv: priv}, nil
}

// Save writes the private key to path with owner-only permissions. The write
// goes to a temporary file first and is renamed into place, so a crash never
// leaves a half written key behind.
func (i *Identity) Save(path string) (err error) {
	der, err := x509.MarshalPKCS8PrivateKey(i.priv)
	if err != nil {
		return fmt.Errorf("identity: encode key: %w", err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: der})

	tmp, err := os.CreateTemp(filepath.Dir(path), ".identity-*")
	if err != nil {
		return fmt.Errorf("identity: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("identity: chmod key: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("identity: write key: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("identity: sync key: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("identity: close key: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("identity: install key: %w", err)
	}
	return nil
}
