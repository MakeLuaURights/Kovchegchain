package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var testParams = Params{ChainID: "test-1", MinFee: 1000}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func addrOf(priv ed25519.PrivateKey) Address {
	return AddressFromPubKey(priv.Public().(ed25519.PublicKey))
}

func signedTx(t *testing.T, priv ed25519.PrivateKey, to Address, amount, nonce uint64) *Tx {
	t.Helper()
	tx := &Tx{ChainID: testParams.ChainID, To: to, Amount: amount, Fee: testParams.MinFee, Nonce: nonce}
	tx.Sign(priv)
	return tx
}

func TestAddressFormat(t *testing.T) {
	a := addrOf(newKey(t))
	if len(a) != AddressLen || !strings.HasPrefix(string(a), "kvg") {
		t.Fatalf("адрес %q неверного вида", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "kvg", strings.ToUpper(string(a)), "abc" + string(a[3:]), string(a[:42]) + "z"} {
		if Address(bad).Validate() == nil {
			t.Errorf("адрес %q принят, хотя неверен", bad)
		}
	}
}

func TestSignatureValid(t *testing.T) {
	priv := newKey(t)
	tx := signedTx(t, priv, addrOf(newKey(t)), 5, 0)
	if err := tx.VerifySignature(); err != nil {
		t.Fatalf("верная подпись не прошла: %v", err)
	}
	if tx.From != addrOf(priv) {
		t.Fatal("Sign не выставил From")
	}
}

func TestSignatureTampered(t *testing.T) {
	priv := newKey(t)
	to := addrOf(newKey(t))

	cases := map[string]func(tx *Tx){
		"сумма":    func(tx *Tx) { tx.Amount++ },
		"комиссия": func(tx *Tx) { tx.Fee++ },
		"nonce":    func(tx *Tx) { tx.Nonce++ },
		"получатель": func(tx *Tx) {
			tx.To = addrOf(newKey(t))
		},
		"сеть":    func(tx *Tx) { tx.ChainID = "other" },
		"подпись": func(tx *Tx) { tx.Signature[0] ^= 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tx := signedTx(t, priv, to, 5, 0)
			mutate(tx)
			if err := tx.VerifySignature(); !errors.Is(err, ErrBadSignature) {
				t.Fatalf("ожидалась ErrBadSignature, получено %v", err)
			}
		})
	}
}

func TestSignatureForeignKey(t *testing.T) {
	victim := newKey(t)
	attacker := newKey(t)
	// Злоумышленник подписывает своим ключом перевод «от имени» чужого адреса.
	tx := &Tx{ChainID: testParams.ChainID, To: addrOf(attacker), Amount: 5, Fee: 1000}
	tx.Sign(attacker)
	tx.From = addrOf(victim)
	if err := tx.VerifySignature(); !errors.Is(err, ErrFromMismatch) {
		t.Fatalf("ожидалась ErrFromMismatch, получено %v", err)
	}
	// Подмена и ключа на ключ жертвы ломает подпись.
	tx.PubKey = HexBytes(victim.Public().(ed25519.PublicKey))
	if err := tx.VerifySignature(); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("ожидалась ErrBadSignature, получено %v", err)
	}
}

func TestTxJSONRoundTrip(t *testing.T) {
	tx := signedTx(t, newKey(t), addrOf(newKey(t)), 123456789012345678, 7)
	data, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"amount":"123456789012345678"`) {
		t.Fatalf("сумма должна быть строкой в JSON: %s", data)
	}
	var back Tx
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Hash() != tx.Hash() || back.VerifySignature() != nil {
		t.Fatal("после JSON транзакция изменилась")
	}
}

func TestApplyTxTransfer(t *testing.T) {
	priv := newKey(t)
	from, to, producer := addrOf(priv), addrOf(newKey(t)), addrOf(newKey(t))
	st := NewOverlay(nil)
	st.Set(from, Account{Balance: 10 * NanoPerKVG})

	tx := signedTx(t, priv, to, 3*NanoPerKVG, 0)
	if err := ApplyTx(st, tx, testParams, producer); err != nil {
		t.Fatal(err)
	}
	f, _ := st.Account(from)
	r, _ := st.Account(to)
	p, _ := st.Account(producer)
	if f.Balance != 7*NanoPerKVG-1000 || f.Nonce != 1 {
		t.Errorf("отправитель: %+v", f)
	}
	if r.Balance != 3*NanoPerKVG || r.Nonce != 0 {
		t.Errorf("получатель: %+v", r)
	}
	if p.Balance != 1000 {
		t.Errorf("производитель: %+v", p)
	}
}

func TestApplyTxRejects(t *testing.T) {
	priv := newKey(t)
	from, to := addrOf(priv), addrOf(newKey(t))
	base := func() *Overlay {
		st := NewOverlay(nil)
		st.Set(from, Account{Balance: 100, Nonce: 2})
		return st
	}
	cases := []struct {
		name string
		tx   func() *Tx
		want error
	}{
		{"nonce из прошлого", func() *Tx { return signedTx(t, priv, to, 1, 1) }, ErrBadNonce},
		{"nonce из будущего", func() *Tx { return signedTx(t, priv, to, 1, 3) }, ErrBadNonce},
		{"не хватает на сумму", func() *Tx { return signedTx(t, priv, to, 100, 2) }, ErrInsufficientFunds},
		{"нулевая сумма", func() *Tx { return signedTx(t, priv, to, 0, 2) }, ErrZeroAmount},
		{"маленькая комиссия", func() *Tx {
			tx := &Tx{ChainID: testParams.ChainID, To: to, Amount: 1, Fee: 999, Nonce: 2}
			tx.Sign(priv)
			return tx
		}, ErrFeeTooLow},
		{"другая сеть", func() *Tx {
			tx := &Tx{ChainID: "other", To: to, Amount: 1, Fee: 1000, Nonce: 2}
			tx.Sign(priv)
			return tx
		}, ErrWrongChain},
		{"переполнение", func() *Tx {
			tx := &Tx{ChainID: testParams.ChainID, To: to, Amount: ^uint64(0), Fee: 1000, Nonce: 2}
			tx.Sign(priv)
			return tx
		}, ErrOverflow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := base()
			before, _ := st.Account(from)
			err := ApplyTx(st, c.tx(), testParams, to)
			if !errors.Is(err, c.want) {
				t.Fatalf("ожидалась %v, получено %v", c.want, err)
			}
			after, _ := st.Account(from)
			if after != before || len(st.Dirty()) != 1 {
				t.Fatal("отклонённая транзакция изменила состояние")
			}
		})
	}
}

func TestParseFormatAmount(t *testing.T) {
	good := map[string]uint64{
		"0":           0,
		"1":           NanoPerKVG,
		"0.5":         NanoPerKVG / 2,
		".5":          NanoPerKVG / 2,
		"1.000000001": NanoPerKVG + 1,
		"1000000000":  1_000_000_000 * NanoPerKVG,
	}
	for s, want := range good {
		got, err := ParseAmount(s)
		if err != nil || got != want {
			t.Errorf("ParseAmount(%q) = %d, %v; ожидалось %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", ".", "1.", "-1", "1e9", "1.0000000001", "abc", "1,5", "99999999999999999999"} {
		if _, err := ParseAmount(s); err == nil {
			t.Errorf("ParseAmount(%q) должна вернуть ошибку", s)
		}
	}
	for nano, want := range map[uint64]string{0: "0", NanoPerKVG: "1", NanoPerKVG + 1: "1.000000001", 1500000000: "1.5"} {
		if got := FormatAmount(nano); got != want {
			t.Errorf("FormatAmount(%d) = %q; ожидалось %q", nano, got, want)
		}
	}
}

func TestBlockHash(t *testing.T) {
	priv := newKey(t)
	b := &Block{Height: 1, Time: 100, Producer: addrOf(priv), Txs: []Tx{*signedTx(t, priv, addrOf(newKey(t)), 1, 0)}}
	b.Seal()
	if b.Hash != b.ComputeHash() || b.TxRoot != TxRoot(b.Txs) {
		t.Fatal("Seal посчитал хэши неверно")
	}
	h := b.Hash
	b.Txs[0].Amount++
	if TxRoot(b.Txs) == b.TxRoot {
		t.Fatal("изменение транзакции не изменило tx_root")
	}
	b.Time++
	if b.ComputeHash() == h {
		t.Fatal("изменение времени не изменило хэш")
	}
}

func TestGenesis(t *testing.T) {
	creator := addrOf(newKey(t))
	g := &Genesis{ChainID: "kovcheg-test", Timestamp: 1700000000, MinFee: "0.001",
		Alloc: []GenesisAlloc{{Address: creator, Amount: "1000000000"}}}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	bal, _ := g.Balances()
	if bal[creator] != 1_000_000_000*NanoPerKVG {
		t.Fatalf("баланс создателя %d", bal[creator])
	}
	b1, _ := g.Block()
	g.Alloc[0].Amount = "1000000001"
	b2, _ := g.Block()
	if b1.Hash == b2.Hash {
		t.Fatal("разные генезисы дали одинаковый хэш")
	}
}
