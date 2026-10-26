package core

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// AddressPrefix — префикс всех адресов сети.
const AddressPrefix = "kvg"

// addressBytes — сколько байт SHA-256 от публичного ключа входит в адрес.
const addressBytes = 20

// AddressLen — длина адреса в символах: "kvg" + 40 hex-символов.
const AddressLen = len(AddressPrefix) + addressBytes*2

// Address — адрес аккаунта: "kvg" + hex первых 20 байт SHA-256 от публичного ключа.
type Address string

// ErrBadAddress — строка не является адресом KVG.
var ErrBadAddress = errors.New("неверный адрес")

// AddressFromPubKey вычисляет адрес по публичному ключу ed25519.
func AddressFromPubKey(pub ed25519.PublicKey) Address {
	sum := sha256.Sum256(pub)
	return Address(AddressPrefix + hex.EncodeToString(sum[:addressBytes]))
}

// ParseAddress проверяет формат адреса. Hex принимается только в нижнем регистре,
// чтобы у одного аккаунта было ровно одно написание адреса.
func ParseAddress(s string) (Address, error) {
	if err := Address(s).Validate(); err != nil {
		return "", err
	}
	return Address(s), nil
}

// Validate проверяет формат адреса.
func (a Address) Validate() error {
	s := string(a)
	if len(s) != AddressLen || !strings.HasPrefix(s, AddressPrefix) {
		return ErrBadAddress
	}
	for _, c := range s[len(AddressPrefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ErrBadAddress
		}
	}
	return nil
}

func (a Address) String() string { return string(a) }
