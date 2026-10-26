package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
	"github.com/makeluaurights/kovchegchain/internal/wallet"
)

func TestEndToEnd(t *testing.T) {
	creator, _ := wallet.New()
	bob, _ := wallet.New()
	g := &core.Genesis{ChainID: "kovcheg-test", Timestamp: 1700000000, MinFee: "0.001",
		Alloc: []core.GenesisAlloc{{Address: creator.Address, Amount: "1000000000"}}}

	store, err := storage.Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c, err := chain.New(store, g, chain.DefaultConfig(creator.Address))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(c))
	defer srv.Close()
	cl := NewClient(srv.URL)

	info, err := cl.Info()
	if err != nil || info.ChainID != "kovcheg-test" || info.MinFee != 1_000_000 {
		t.Fatalf("info: %+v %v", info, err)
	}
	bal, err := cl.Balance(creator.Address)
	if err != nil || bal.BalanceKVG != "1000000000" {
		t.Fatalf("balance: %+v %v", bal, err)
	}

	tx := &core.Tx{ChainID: info.ChainID, To: bob.Address, Amount: 25 * core.NanoPerKVG, Fee: info.MinFee, Nonce: bal.PendingNonce}
	tx.Sign(creator.Private)
	h, err := cl.SubmitTx(tx)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := cl.Tx(h); err != nil || r.Status != chain.TxPending {
		t.Fatalf("tx до блока: %+v %v", r, err)
	}
	if _, err := cl.SubmitTx(tx); err == nil || !strings.Contains(err.Error(), "уже в мемпуле") {
		t.Fatalf("повтор должен отклоняться: %v", err)
	}

	if _, err := c.ProduceBlock(time.Unix(1700000005, 0)); err != nil {
		t.Fatal(err)
	}
	if r, err := cl.Tx(h); err != nil || r.Status != chain.TxConfirmed || r.BlockHeight != 1 {
		t.Fatalf("tx после блока: %+v %v", r, err)
	}
	if b, err := cl.Balance(bob.Address); err != nil || b.BalanceKVG != "25" {
		t.Fatalf("баланс получателя: %+v %v", b, err)
	}

	for _, path := range []string{"/block", "/block?height=0", "/block?height=1"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %v %v", path, resp.StatusCode, err)
		}
		resp.Body.Close()
	}
	for path, code := range map[string]int{
		"/block?height=99":    http.StatusNotFound,
		"/block?height=x":     http.StatusBadRequest,
		"/balance?address=xx": http.StatusBadRequest,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil || resp.StatusCode != code {
			t.Fatalf("%s: ожидался %d, получен %v %v", path, code, resp.StatusCode, err)
		}
		resp.Body.Close()
	}
}
