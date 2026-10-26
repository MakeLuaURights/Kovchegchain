package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Hash — SHA-256 хэш.
type Hash [32]byte

// IsZero — true для нулевого хэша.
func (h Hash) IsZero() bool { return h == Hash{} }

func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// ParseHash разбирает хэш из hex-строки (64 символа).
func ParseHash(s string) (Hash, error) {
	var h Hash
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(h) {
		return h, errors.New("неверный хэш")
	}
	copy(h[:], b)
	return h, nil
}

func (h Hash) MarshalJSON() ([]byte, error) { return json.Marshal(h.String()) }

func (h *Hash) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	p, err := ParseHash(s)
	if err != nil {
		return err
	}
	*h = p
	return nil
}

// encoder собирает каноническое двоичное представление для хэширования и подписи.
// Строки и байты пишутся с префиксом длины, числа — big-endian фиксированной ширины,
// поэтому разные наборы полей не могут дать одинаковые байты.
type encoder struct{ buf []byte }

func (e *encoder) bytes(b []byte) {
	e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(len(b)))
	e.buf = append(e.buf, b...)
}

func (e *encoder) str(s string) { e.bytes([]byte(s)) }

func (e *encoder) u64(v uint64) { e.buf = binary.BigEndian.AppendUint64(e.buf, v) }

func (e *encoder) i64(v int64) { e.u64(uint64(v)) }

func (e *encoder) hash(h Hash) { e.buf = append(e.buf, h[:]...) }

func (e *encoder) sum() Hash { return sha256.Sum256(e.buf) }
