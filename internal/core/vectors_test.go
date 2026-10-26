package core

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// Тестовые векторы подписи: testdata/sign_vectors.json. По ним клиент на другом языке
// (мини-апка на JavaScript, бот) проверяет, что собирает и подписывает транзакции так же, как узел.
// Пересоздать файл: go test ./internal/core -run TestSigningVectors -update
var update = flag.Bool("update", false, "перезаписать testdata/sign_vectors.json")

type vector struct {
	Name         string `json:"name"`
	Seed         string `json:"seed"` // 32 байта закрытого ключа (seed ed25519)
	PubKey       string `json:"pub_key"`
	From         string `json:"from"`
	Tx           Tx     `json:"tx"`
	SigningBytes string `json:"signing_bytes"`
	Hash         string `json:"hash"`
}

func vectorInputs() []vector {
	seed := func(b byte) string {
		s := make([]byte, ed25519.SeedSize)
		for i := range s {
			s[i] = b + byte(i)
		}
		return hex.EncodeToString(s)
	}
	return []vector{
		{Name: "простой перевод 12.5 KVG", Seed: seed(1), Tx: Tx{
			ChainID: "kovcheg-local-1", To: "kvg0000000000000000000000000000000000000001",
			Amount: 12_500_000_000, Fee: 1_000_000, Nonce: 0,
		}},
		{Name: "большие числа", Seed: seed(100), Tx: Tx{
			ChainID: "kovcheg-test", To: "kvgffffffffffffffffffffffffffffffffffffffff",
			Amount: 1_000_000_000_000_000_000, Fee: 18_446_744_073, Nonce: 4_294_967_296,
		}},
		{Name: "chain_id в юникоде", Seed: seed(200), Tx: Tx{
			ChainID: "ковчег-1", To: "kvg0123456789abcdef0123456789abcdef01234567",
			Amount: 1, Fee: 0, Nonce: 7,
		}},
	}
}

func TestSigningVectors(t *testing.T) {
	path := filepath.Join("testdata", "sign_vectors.json")
	if *update {
		var out []vector
		for _, v := range vectorInputs() {
			s, _ := hex.DecodeString(v.Seed)
			priv := ed25519.NewKeyFromSeed(s)
			v.Tx.Sign(priv)
			v.PubKey = v.Tx.PubKey.String()
			v.From = string(v.Tx.From)
			v.SigningBytes = hex.EncodeToString(v.Tx.SigningBytes())
			v.Hash = v.Tx.Hash().String()
			out = append(out, v)
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(data, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) != len(vectorInputs()) {
		t.Fatalf("в файле %d векторов, ожидалось %d", len(vs), len(vectorInputs()))
	}
	for _, v := range vs {
		t.Run(v.Name, func(t *testing.T) {
			s, _ := hex.DecodeString(v.Seed)
			priv := ed25519.NewKeyFromSeed(s)
			if got := hex.EncodeToString(priv.Public().(ed25519.PublicKey)); got != v.PubKey {
				t.Fatalf("pub_key %s", got)
			}
			if got := AddressFromPubKey(priv.Public().(ed25519.PublicKey)); string(got) != v.From {
				t.Fatalf("адрес %s", got)
			}
			if got := hex.EncodeToString(v.Tx.SigningBytes()); got != v.SigningBytes {
				t.Fatalf("signing_bytes\n%s\n%s", got, v.SigningBytes)
			}
			if got := v.Tx.Hash().String(); got != v.Hash {
				t.Fatalf("hash %s", got)
			}
			if err := v.Tx.VerifySignature(); err != nil {
				t.Fatal(err)
			}
			// ed25519 детерминирован: повторная подпись даёт те же байты.
			again := v.Tx
			again.Sign(priv)
			if again.Signature.String() != v.Tx.Signature.String() {
				t.Fatal("подпись не совпала с вектором")
			}
		})
	}
}
