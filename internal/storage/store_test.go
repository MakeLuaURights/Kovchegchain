package storage

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/makeluaurights/kovchegchain/internal/core"
)

type fixture struct {
	path  string
	s     *Store
	alice ed25519.PrivateKey
	bob   core.Address
}

func key(t *testing.T) ed25519.PrivateKey {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func addr(k ed25519.PrivateKey) core.Address {
	return core.AddressFromPubKey(k.Public().(ed25519.PublicKey))
}

// newFixture: генезис + 3 блока; в блоке 2 две транзакции alice -> bob, в блоке 3 одна bob -> alice
// (подписана чужим ключом — хранилище подписи не проверяет, ему важны только адреса).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{path: filepath.Join(t.TempDir(), "chain.db"), alice: key(t), bob: addr(key(t))}
	s, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.s = s
	t.Cleanup(func() { f.s.Close() })

	a := addr(f.alice)
	mk := func(from, to core.Address, nonce uint64) core.Tx {
		tx := core.Tx{ChainID: "t", To: to, Amount: 1, Fee: 0, Nonce: nonce}
		tx.Sign(f.alice)
		tx.From = from
		return tx
	}
	blocks := [][]core.Tx{
		nil,
		nil,
		{mk(a, f.bob, 0), mk(a, f.bob, 1)},
		{mk(f.bob, a, 0)},
	}
	var prev core.Hash
	for h, txs := range blocks {
		b := &core.Block{Height: uint64(h), Time: int64(h), PrevHash: prev, Producer: a, Txs: txs}
		b.Seal()
		accs := map[core.Address]core.Account{}
		if h == 0 {
			accs[a] = core.Account{Balance: 100}
		}
		if h == 2 {
			accs[f.bob] = core.Account{Balance: 2}
		}
		if err := s.CommitBlock(b, accs); err != nil {
			t.Fatal(err)
		}
		prev = b.Hash
	}
	return f
}

func history(t *testing.T, s *Store, a core.Address, limit int) string {
	t.Helper()
	var out []string
	var before *TxLocation
	for {
		recs, more, err := s.AddressTxs(a, before, limit)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			out = append(out, fmt.Sprintf("%d.%d", r.Height, r.Index))
		}
		if !more {
			return strings.Join(out, " ")
		}
		before = &recs[len(recs)-1].TxLocation
	}
}

func TestAddressTxs(t *testing.T) {
	f := newFixture(t)
	for _, limit := range []int{1, 2, 10} {
		if got := history(t, f.s, f.bob, limit); got != "3.0 2.1 2.0" {
			t.Fatalf("limit %d: %s", limit, got)
		}
	}
	if got := history(t, f.s, addr(f.alice), 10); got != "3.0 2.1 2.0" {
		t.Fatalf("alice: %s", got)
	}
	if got := history(t, f.s, addr(key(t)), 10); got != "" {
		t.Fatalf("чужой адрес: %s", got)
	}
	// before посередине: только более старые.
	recs, more, _ := f.s.AddressTxs(f.bob, &TxLocation{Height: 2, Index: 1}, 10)
	if len(recs) != 1 || recs[0].Height != 2 || recs[0].Index != 0 || more {
		t.Fatalf("before 2.1: %+v %v", recs, more)
	}
}

func TestStats(t *testing.T) {
	f := newFixture(t)
	st, err := f.s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.TxCount != 3 || st.AccountCount != 2 {
		t.Fatalf("%+v", st)
	}
}

// TestUpgradeFromStage1 — база этапа 1 (без addrtxs и счётчиков) при открытии получает индексы.
func TestUpgradeFromStage1(t *testing.T) {
	f := newFixture(t)
	err := f.s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bucketAddrTxs); err != nil {
			return err
		}
		meta := tx.Bucket(bucketMeta)
		for _, k := range [][]byte{keyIndexVersion, keyTxCount, keyAccountCount} {
			if err := meta.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.Close()

	s, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.s = s
	if got := history(t, s, f.bob, 10); got != "3.0 2.1 2.0" {
		t.Fatalf("история после обновления: %s", got)
	}
	if st, _ := s.Stats(); st.TxCount != 3 || st.AccountCount != 2 {
		t.Fatalf("счётчики после обновления: %+v", st)
	}
	b, err := s.BlockByHeight(3)
	if err != nil {
		t.Fatal(err)
	}
	h := b.Txs[0].Hash()
	if loc, err := s.TxLocation(h); err != nil || loc.Height != 3 {
		t.Fatalf("индекс транзакций: %+v %v", loc, err)
	}
}

func TestCommitOrder(t *testing.T) {
	f := newFixture(t)
	b := &core.Block{Height: 7}
	b.Seal()
	if err := f.s.CommitBlock(b, nil); err == nil {
		t.Fatal("блок не по порядку записан")
	}
}
