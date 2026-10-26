package chain

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
)

type env struct {
	t        *testing.T
	path     string
	store    *storage.Store
	genesis  *core.Genesis
	chain    *Chain
	creator  ed25519.PrivateKey
	producer core.Address
	now      time.Time
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func addrOf(priv ed25519.PrivateKey) core.Address {
	return core.AddressFromPubKey(priv.Public().(ed25519.PublicKey))
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, path: filepath.Join(t.TempDir(), "chain.db"), creator: newKey(t), now: time.Unix(1700000100, 0)}
	e.producer = addrOf(newKey(t))
	e.genesis = &core.Genesis{
		ChainID: "kovcheg-test", Timestamp: 1700000000, MinFee: "0.001",
		Alloc: []core.GenesisAlloc{{Address: addrOf(e.creator), Amount: "1000000000"}},
	}
	e.open()
	t.Cleanup(func() { e.store.Close() })
	return e
}

func (e *env) open() {
	e.t.Helper()
	s, err := storage.Open(e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := New(s, e.genesis, DefaultConfig(e.producer))
	if err != nil {
		e.t.Fatal(err)
	}
	e.store, e.chain = s, c
}

func (e *env) reopen() {
	e.t.Helper()
	e.store.Close()
	e.open()
}

func (e *env) tx(from ed25519.PrivateKey, to core.Address, kvg, nonce uint64) *core.Tx {
	tx := &core.Tx{ChainID: e.genesis.ChainID, To: to, Amount: kvg * core.NanoPerKVG, Fee: fee, Nonce: nonce}
	tx.Sign(from)
	return tx
}

func (e *env) produce() *core.Block {
	e.t.Helper()
	e.now = e.now.Add(5 * time.Second)
	b, err := e.chain.ProduceBlock(e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *env) balance(a core.Address) uint64 {
	e.t.Helper()
	acc, err := e.chain.Account(a)
	if err != nil {
		e.t.Fatal(err)
	}
	return acc.Balance
}

const fee = 1_000_000 // 0.001 KVG
const supply = 1_000_000_000 * core.NanoPerKVG

func TestGenesisBalance(t *testing.T) {
	e := newEnv(t)
	if got := e.balance(addrOf(e.creator)); got != supply {
		t.Fatalf("баланс создателя %d, ожидалось %d", got, supply)
	}
	if h := e.chain.Head(); h.Height != 0 {
		t.Fatalf("высота %d", h.Height)
	}
}

func TestTransfer(t *testing.T) {
	e := newEnv(t)
	alice := newKey(t)
	creator := addrOf(e.creator)

	if _, err := e.chain.SubmitTx(e.tx(e.creator, addrOf(alice), 100, 0)); err != nil {
		t.Fatal(err)
	}
	// До блока подтверждённый баланс не меняется.
	if e.balance(addrOf(alice)) != 0 {
		t.Fatal("баланс изменился до блока")
	}
	b := e.produce()
	if b == nil || b.Height != 1 || len(b.Txs) != 1 {
		t.Fatalf("неверный блок: %+v", b)
	}
	if got := e.balance(addrOf(alice)); got != 100*core.NanoPerKVG {
		t.Fatalf("alice: %d", got)
	}
	if got := e.balance(creator); got != supply-100*core.NanoPerKVG-fee {
		t.Fatalf("создатель: %d", got)
	}
	if got := e.balance(e.producer); got != fee {
		t.Fatalf("производитель: %d", got)
	}

	// Alice переводит дальше — цепочка переводов в следующих блоках.
	bob := addrOf(newKey(t))
	if _, err := e.chain.SubmitTx(e.tx(alice, bob, 40, 0)); err != nil {
		t.Fatal(err)
	}
	e.produce()
	if got := e.balance(bob); got != 40*core.NanoPerKVG {
		t.Fatalf("bob: %d", got)
	}

	// Сумма всех балансов не меняется: монеты не создаются и не исчезают.
	total := e.balance(creator) + e.balance(addrOf(alice)) + e.balance(bob) + e.balance(e.producer)
	if total != supply {
		t.Fatalf("общая сумма %d, ожидалось %d", total, supply)
	}
}

func TestSeveralTxsInOneBlock(t *testing.T) {
	e := newEnv(t)
	to := addrOf(newKey(t))
	for n := uint64(0); n < 5; n++ {
		if _, err := e.chain.SubmitTx(e.tx(e.creator, to, 1, n)); err != nil {
			t.Fatalf("nonce %d: %v", n, err)
		}
	}
	pend, _ := e.chain.PendingAccount(addrOf(e.creator))
	if pend.Nonce != 5 {
		t.Fatalf("pending nonce %d", pend.Nonce)
	}
	if b := e.produce(); len(b.Txs) != 5 {
		t.Fatalf("в блоке %d транзакций", len(b.Txs))
	}
	if e.balance(to) != 5*core.NanoPerKVG || e.chain.MempoolSize() != 0 {
		t.Fatal("не все транзакции применены")
	}
}

func TestDoubleSpendSameNonce(t *testing.T) {
	e := newEnv(t)
	bob, carol := addrOf(newKey(t)), addrOf(newKey(t))

	if _, err := e.chain.SubmitTx(e.tx(e.creator, bob, 10, 0)); err != nil {
		t.Fatal(err)
	}
	// Вторая транзакция с тем же nonce — попытка потратить одно и то же дважды.
	if _, err := e.chain.SubmitTx(e.tx(e.creator, carol, 10, 0)); !errors.Is(err, core.ErrBadNonce) {
		t.Fatalf("ожидалась ErrBadNonce, получено %v", err)
	}
	e.produce()
	if e.balance(carol) != 0 {
		t.Fatal("двойная трата прошла")
	}
}

func TestDoubleSpendBalance(t *testing.T) {
	e := newEnv(t)
	alice := newKey(t)
	bob, carol := addrOf(newKey(t)), addrOf(newKey(t))

	// У Alice ровно 10 KVG + комиссия на одну транзакцию.
	fund := &core.Tx{ChainID: e.genesis.ChainID, To: addrOf(alice), Amount: 10*core.NanoPerKVG + fee, Fee: fee, Nonce: 0}
	fund.Sign(e.creator)
	if _, err := e.chain.SubmitTx(fund); err != nil {
		t.Fatal(err)
	}
	e.produce()

	if _, err := e.chain.SubmitTx(e.tx(alice, bob, 10, 0)); err != nil {
		t.Fatal(err)
	}
	// Мемпул учитывает уже ожидающую транзакцию: на вторую денег нет.
	if _, err := e.chain.SubmitTx(e.tx(alice, carol, 10, 1)); !errors.Is(err, core.ErrInsufficientFunds) {
		t.Fatalf("ожидалась ErrInsufficientFunds, получено %v", err)
	}
	e.produce()
	if e.balance(bob) != 10*core.NanoPerKVG || e.balance(carol) != 0 || e.balance(addrOf(alice)) != 0 {
		t.Fatal("неверные балансы после попытки двойной траты")
	}
}

func TestReplay(t *testing.T) {
	e := newEnv(t)
	tx := e.tx(e.creator, addrOf(newKey(t)), 1, 0)
	if _, err := e.chain.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.chain.SubmitTx(tx); !errors.Is(err, ErrDuplicateTx) {
		t.Fatalf("повтор в мемпуле: ожидалась ErrDuplicateTx, получено %v", err)
	}
	e.produce()
	if _, err := e.chain.SubmitTx(tx); !errors.Is(err, ErrAlreadyInChain) {
		t.Fatalf("повтор после блока: ожидалась ErrAlreadyInChain, получено %v", err)
	}
}

func TestWrongNonce(t *testing.T) {
	e := newEnv(t)
	to := addrOf(newKey(t))
	if _, err := e.chain.SubmitTx(e.tx(e.creator, to, 1, 1)); !errors.Is(err, core.ErrBadNonce) {
		t.Fatalf("nonce из будущего: ожидалась ErrBadNonce, получено %v", err)
	}
	if _, err := e.chain.SubmitTx(e.tx(e.creator, to, 1, 0)); err != nil {
		t.Fatal(err)
	}
	e.produce()
	// Тот же nonce 0, но другая сумма — другая транзакция, nonce уже использован.
	if _, err := e.chain.SubmitTx(e.tx(e.creator, to, 2, 0)); !errors.Is(err, core.ErrBadNonce) {
		t.Fatalf("nonce из прошлого: ожидалась ErrBadNonce, получено %v", err)
	}
	if e.chain.MempoolSize() != 0 {
		t.Fatal("неверные транзакции попали в мемпул")
	}
}

func TestBadSignatureRejected(t *testing.T) {
	e := newEnv(t)
	tx := e.tx(e.creator, addrOf(newKey(t)), 1, 0)
	tx.Amount = 500 * core.NanoPerKVG // подмена суммы после подписи
	if _, err := e.chain.SubmitTx(tx); !errors.Is(err, core.ErrBadSignature) {
		t.Fatalf("ожидалась ErrBadSignature, получено %v", err)
	}
}

func TestEmptyBlocksSkipped(t *testing.T) {
	e := newEnv(t)
	if b := e.produce(); b != nil {
		t.Fatalf("собран пустой блок #%d", b.Height)
	}
}

func TestAddBlockRejectsInvalid(t *testing.T) {
	e := newEnv(t)
	head := e.chain.Head()
	good := func() *core.Block {
		b := &core.Block{Height: head.Height + 1, Time: head.Time + 5, PrevHash: head.Hash,
			Producer: e.producer, Txs: []core.Tx{*e.tx(e.creator, e.producer, 1, 0)}}
		b.Seal()
		return b
	}
	cases := map[string]func(b *core.Block){
		"номер":           func(b *core.Block) { b.Height = 5; b.Hash = b.ComputeHash() },
		"предыдущий хэш":  func(b *core.Block) { b.PrevHash = core.Hash{1}; b.Hash = b.ComputeHash() },
		"время в прошлом": func(b *core.Block) { b.Time = head.Time - 1; b.Hash = b.ComputeHash() },
		"хэш":             func(b *core.Block) { b.Hash[0] ^= 1 },
		"подменённая tx":  func(b *core.Block) { b.Txs[0].Amount *= 2 },
		"двойная трата в блоке": func(b *core.Block) {
			b.Txs = append(b.Txs, *e.tx(e.creator, e.producer, 2, 0))
			b.Seal()
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := good()
			mutate(b)
			if err := e.chain.AddBlock(b); !errors.Is(err, ErrBadBlock) {
				t.Fatalf("ожидалась ErrBadBlock, получено %v", err)
			}
		})
	}
	if err := e.chain.AddBlock(good()); err != nil {
		t.Fatalf("верный блок отклонён: %v", err)
	}
}

func TestPersistence(t *testing.T) {
	e := newEnv(t)
	to := addrOf(newKey(t))
	h, err := e.chain.SubmitTx(e.tx(e.creator, to, 7, 0))
	if err != nil {
		t.Fatal(err)
	}
	b := e.produce()

	e.reopen()
	if got := e.chain.Head(); got.Hash != b.Hash {
		t.Fatalf("после перезапуска голова %s, ожидалась %s", got.Hash, b.Hash)
	}
	if e.balance(to) != 7*core.NanoPerKVG {
		t.Fatal("баланс потерян после перезапуска")
	}
	if _, status, height, err := e.chain.Tx(h); err != nil || status != TxConfirmed || height != 1 {
		t.Fatalf("транзакция после перезапуска: %v %v %d", err, status, height)
	}
	if _, err := e.chain.SubmitTx(e.tx(e.creator, to, 1, 0)); !errors.Is(err, core.ErrBadNonce) {
		t.Fatalf("nonce не сохранился: %v", err)
	}
}

func TestGenesisMismatch(t *testing.T) {
	e := newEnv(t)
	e.store.Close()
	e.genesis.Alloc[0].Amount = "5"
	s, err := storage.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	e.store = s
	if _, err := New(s, e.genesis, DefaultConfig(e.producer)); !errors.Is(err, ErrGenesisChanged) {
		t.Fatalf("ожидалась ErrGenesisChanged, получено %v", err)
	}
}
