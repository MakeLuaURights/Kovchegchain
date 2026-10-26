// Package wallet — ключи ed25519 и файл кошелька.
package wallet

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/makeluaurights/kovchegchain/internal/core"
)

// Wallet — пара ключей и адрес.
type Wallet struct {
	Address core.Address
	Private ed25519.PrivateKey
}

// file — формат wallet.json. Хранится только seed (32 байта): из него восстанавливается вся пара.
type file struct {
	Address   core.Address `json:"address"`
	PublicKey string       `json:"public_key"`
	Seed      string       `json:"private_seed"`
}

// New создаёт новый кошелёк со случайным ключом.
func New() (*Wallet, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return FromPrivate(priv), nil
}

// FromPrivate собирает кошелёк из закрытого ключа.
func FromPrivate(priv ed25519.PrivateKey) *Wallet {
	return &Wallet{Address: core.AddressFromPubKey(priv.Public().(ed25519.PublicKey)), Private: priv}
}

// PublicKey — публичный ключ кошелька.
func (w *Wallet) PublicKey() ed25519.PublicKey { return w.Private.Public().(ed25519.PublicKey) }

// Save записывает кошелёк в файл с правами 0600. Существующий файл не перезаписывается.
func (w *Wallet) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file{
		Address:   w.Address,
		PublicKey: hex.EncodeToString(w.PublicKey()),
		Seed:      hex.EncodeToString(w.Private.Seed()),
	}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("файл %s уже существует — старый кошелёк не перезаписан", path)
		}
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load читает кошелёк из файла и проверяет, что адрес соответствует ключу.
func Load(path string) (*Wallet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seed, err := hex.DecodeString(f.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: неверный private_seed", path)
	}
	w := FromPrivate(ed25519.NewKeyFromSeed(seed))
	if f.Address != "" && f.Address != w.Address {
		return nil, fmt.Errorf("%s: адрес в файле не соответствует ключу", path)
	}
	return w, nil
}
