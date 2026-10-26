package core

import (
	"crypto/ed25519"
	"errors"
	"fmt"
)

// Ошибки проверки транзакций.
var (
	ErrBadSignature      = errors.New("неверная подпись")
	ErrFromMismatch      = errors.New("адрес отправителя не соответствует публичному ключу")
	ErrBadNonce          = errors.New("неверный nonce")
	ErrInsufficientFunds = errors.New("недостаточно средств")
	ErrZeroAmount        = errors.New("сумма перевода должна быть больше нуля")
	ErrFeeTooLow         = errors.New("слишком маленькая комиссия")
	ErrWrongChain        = errors.New("транзакция для другой сети")
	ErrOverflow          = errors.New("переполнение суммы")
)

// Tx — перевод KVG. Суммы в нано-KVG. В JSON числа пишутся строками,
// чтобы JavaScript-клиенты (мини-апка, сайт) не теряли точность на uint64.
type Tx struct {
	ChainID   string   `json:"chain_id"`
	From      Address  `json:"from"`
	To        Address  `json:"to"`
	Amount    uint64   `json:"amount,string"`
	Fee       uint64   `json:"fee,string"`
	Nonce     uint64   `json:"nonce,string"`
	PubKey    HexBytes `json:"pub_key"`
	Signature HexBytes `json:"signature"`
}

// SigningBytes — байты, которые подписывает отправитель (всё, кроме подписи).
func (tx *Tx) SigningBytes() []byte {
	e := encoder{}
	e.str("KVG-TX-v1")
	e.str(tx.ChainID)
	e.str(string(tx.From))
	e.str(string(tx.To))
	e.u64(tx.Amount)
	e.u64(tx.Fee)
	e.u64(tx.Nonce)
	e.bytes(tx.PubKey)
	return e.buf
}

// Hash — идентификатор транзакции: SHA-256 от подписываемых байт.
// Подпись ed25519 в Go не допускает подмены (S проверяется на каноничность),
// поэтому одной и той же транзакции соответствует ровно один хэш.
func (tx *Tx) Hash() Hash {
	e := encoder{buf: tx.SigningBytes()}
	return e.sum()
}

// Sign заполняет PubKey, From и Signature закрытым ключом отправителя.
func (tx *Tx) Sign(priv ed25519.PrivateKey) {
	pub := priv.Public().(ed25519.PublicKey)
	tx.PubKey = HexBytes(pub)
	tx.From = AddressFromPubKey(pub)
	tx.Signature = ed25519.Sign(priv, tx.SigningBytes())
}

// VerifySignature проверяет, что подпись верна и ключ принадлежит адресу From.
func (tx *Tx) VerifySignature() error {
	if len(tx.PubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: длина публичного ключа", ErrBadSignature)
	}
	if len(tx.Signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: длина подписи", ErrBadSignature)
	}
	if AddressFromPubKey(ed25519.PublicKey(tx.PubKey)) != tx.From {
		return ErrFromMismatch
	}
	if !ed25519.Verify(ed25519.PublicKey(tx.PubKey), tx.SigningBytes(), tx.Signature) {
		return ErrBadSignature
	}
	return nil
}

// ValidateBasic — проверки, не зависящие от состояния: формат, сеть, комиссия, подпись.
func (tx *Tx) ValidateBasic(p Params) error {
	if tx.ChainID != p.ChainID {
		return ErrWrongChain
	}
	if err := tx.From.Validate(); err != nil {
		return fmt.Errorf("отправитель: %w", err)
	}
	if err := tx.To.Validate(); err != nil {
		return fmt.Errorf("получатель: %w", err)
	}
	if tx.Amount == 0 {
		return ErrZeroAmount
	}
	if tx.Fee < p.MinFee {
		return fmt.Errorf("%w: минимум %s KVG", ErrFeeTooLow, FormatAmount(p.MinFee))
	}
	if tx.Amount > ^uint64(0)-tx.Fee {
		return ErrOverflow
	}
	return tx.VerifySignature()
}
