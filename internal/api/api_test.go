package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
	"github.com/makeluaurights/kovchegchain/internal/wallet"
)

type testNode struct {
	t       *testing.T
	chain   *chain.Chain
	srv     *httptest.Server
	cl      *Client
	creator *wallet.Wallet
	params  core.Params

	mu  sync.Mutex
	now time.Time
}

func newTestNode(t *testing.T, mutate func(*Options)) *testNode {
	t.Helper()
	creator, _ := wallet.New()
	g := &core.Genesis{ChainID: "kovcheg-test", Timestamp: 1700000000, MinFee: "0.001",
		Alloc: []core.GenesisAlloc{{Address: creator.Address, Amount: "1000000000"}}}
	store, err := storage.Open(filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	c, err := chain.New(store, g, chain.DefaultConfig(creator.Address))
	if err != nil {
		t.Fatal(err)
	}
	opt := DefaultOptions()
	opt.RateLimit = 0
	opt.Version = "test"
	if mutate != nil {
		mutate(&opt)
	}
	srv := httptest.NewServer(NewHandler(c, opt))
	t.Cleanup(srv.Close)
	return &testNode{t: t, chain: c, srv: srv, cl: NewClient(srv.URL), creator: creator,
		params: g.Params(), now: time.Unix(1700000000, 0)}
}

func (n *testNode) produce() *core.Block {
	n.t.Helper()
	n.mu.Lock()
	n.now = n.now.Add(5 * time.Second)
	now := n.now
	n.mu.Unlock()
	b, err := n.chain.ProduceBlock(now)
	if err != nil {
		n.t.Error(err)
	}
	return b
}

func (n *testNode) tx(from *wallet.Wallet, to core.Address, kvg, nonce uint64) *core.Tx {
	tx := &core.Tx{ChainID: n.params.ChainID, To: to, Amount: kvg * core.NanoPerKVG, Fee: n.params.MinFee, Nonce: nonce}
	tx.Sign(from.Private)
	return tx
}

// get делает GET и возвращает статус и тело.
func (n *testNode) get(path string) (int, []byte) {
	n.t.Helper()
	resp, err := http.Get(n.srv.URL + path)
	if err != nil {
		n.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func (n *testNode) getJSON(path string, out any) {
	n.t.Helper()
	code, body := n.get(path)
	if code != http.StatusOK {
		n.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		n.t.Fatalf("GET %s: %v", path, err)
	}
}

// wantError проверяет статус и код ошибки.
func wantError(t *testing.T, code int, body []byte, wantCode int, wantName string) {
	t.Helper()
	var e ErrorView
	_ = json.Unmarshal(body, &e)
	if code != wantCode || e.Code != wantName || e.Error == "" {
		t.Fatalf("ожидалось %d %s, получено %d %s", wantCode, wantName, code, body)
	}
}

func apiCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestStatus(t *testing.T) {
	n := newTestNode(t, nil)
	st, err := n.cl.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.ChainID != "kovcheg-test" || st.Height != 0 || st.MinFee != 1_000_000 || st.MinFeeKVG != "0.001" ||
		st.SupplyKVG != "1000000000" || st.AccountCount != 1 || st.TxCount != 0 || st.Version != "test" ||
		st.GenesisHash != st.HeadHash || st.BlockInterval != 5 {
		t.Fatalf("неверный статус: %+v", st)
	}
	_, body := n.get("/v1/status")
	if !strings.Contains(string(body), `"supply": "1000000000000000000"`) {
		t.Fatalf("supply должен быть строкой: %s", body)
	}
}

func TestAccountAndPending(t *testing.T) {
	n := newTestNode(t, nil)
	bob, _ := wallet.New()

	if _, err := n.cl.SubmitTx(n.tx(n.creator, bob.Address, 10, 0), false, 0); err != nil {
		t.Fatal(err)
	}
	a, err := n.cl.Account(n.creator.Address)
	if err != nil {
		t.Fatal(err)
	}
	if a.BalanceKVG != "1000000000" || a.PendingBalanceKVG != "999999990" || a.PendingNonce != 1 || a.Nonce != 0 || a.PendingTxs != 1 {
		// производитель — сам создатель, поэтому комиссия возвращается ему же
		t.Fatalf("до блока: %+v", a)
	}
	n.produce()
	a, _ = n.cl.Account(n.creator.Address)
	if a.BalanceKVG != "999999990" || a.Nonce != 1 || a.PendingTxs != 0 {
		t.Fatalf("после блока: %+v", a)
	}
	b, _ := n.cl.Account(bob.Address)
	if b.BalanceKVG != "10" {
		t.Fatalf("bob: %+v", b)
	}
	// Никому не известный адрес — нулевой баланс, а не ошибка.
	empty, _ := wallet.New()
	if e, err := n.cl.Account(empty.Address); err != nil || e.Balance != 0 {
		t.Fatalf("пустой адрес: %+v %v", e, err)
	}
	code, body := n.get("/v1/accounts/kvgXYZ")
	wantError(t, code, body, 400, "bad_address")
}

func TestHistory(t *testing.T) {
	n := newTestNode(t, nil)
	bob, _ := wallet.New()

	// Блок 1: две транзакции создатель -> bob. Блок 2: bob -> создатель. Затем одна в мемпуле.
	n.cl.SubmitTx(n.tx(n.creator, bob.Address, 10, 0), false, 0)
	n.cl.SubmitTx(n.tx(n.creator, bob.Address, 20, 1), false, 0)
	n.produce()
	n.cl.SubmitTx(n.tx(bob, n.creator.Address, 5, 0), false, 0)
	n.produce()
	n.cl.SubmitTx(n.tx(bob, bob.Address, 1, 1), false, 0) // самому себе

	list, err := n.cl.AccountTxs(bob.Address, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Pending) != 1 || list.Pending[0].Direction != "self" || list.Pending[0].Status != chain.TxPending {
		t.Fatalf("pending: %+v", list.Pending)
	}
	var got []string
	for _, tx := range list.Txs {
		got = append(got, fmt.Sprintf("%d.%d:%s:%s", tx.Block.Height, tx.Block.Index, tx.Direction, tx.AmountKVG))
	}
	want := "2.0:out:5 1.1:in:20 1.0:in:10"
	if strings.Join(got, " ") != want || list.Next != "" {
		t.Fatalf("история: %v next=%q, ожидалось %s", got, list.Next, want)
	}

	// Постранично по одной записи.
	var pages []string
	before := ""
	for i := 0; i < 5; i++ {
		p, err := n.cl.AccountTxs(bob.Address, before, 1)
		if err != nil {
			t.Fatal(err)
		}
		if before != "" && len(p.Pending) != 0 {
			t.Fatal("pending должен быть только на первой странице")
		}
		for _, tx := range p.Txs {
			pages = append(pages, fmt.Sprintf("%d.%d", tx.Block.Height, tx.Block.Index))
		}
		if p.Next == "" {
			break
		}
		before = p.Next
	}
	if strings.Join(pages, " ") != "2.0 1.1 1.0" {
		t.Fatalf("постранично: %v", pages)
	}

	// История создателя: подтверждения считаются от головы.
	cl, _ := n.cl.AccountTxs(n.creator.Address, "", 10)
	if len(cl.Txs) != 3 || cl.Txs[0].Direction != "in" || cl.Txs[2].Confirmations != 2 {
		t.Fatalf("история создателя: %+v", cl.Txs)
	}

	code, body := n.get("/v1/accounts/" + string(bob.Address) + "/txs?before=abc")
	wantError(t, code, body, 400, "bad_request")
	code, body = n.get("/v1/accounts/" + string(bob.Address) + "/txs?limit=0")
	wantError(t, code, body, 400, "bad_request")
}

func TestSubmitErrors(t *testing.T) {
	n := newTestNode(t, nil)
	bob, _ := wallet.New()

	cases := []struct {
		name string
		tx   func() *core.Tx
		code string
	}{
		{"неверный nonce", func() *core.Tx { return n.tx(n.creator, bob.Address, 1, 5) }, "bad_nonce"},
		{"нет денег", func() *core.Tx { return n.tx(bob, n.creator.Address, 1, 0) }, "insufficient_funds"},
		{"подпись", func() *core.Tx {
			tx := n.tx(n.creator, bob.Address, 1, 0)
			tx.Amount++
			return tx
		}, "bad_signature"},
		{"чужой адрес", func() *core.Tx {
			tx := n.tx(bob, n.creator.Address, 1, 0)
			tx.From = n.creator.Address
			return tx
		}, "from_mismatch"},
		{"комиссия", func() *core.Tx {
			tx := &core.Tx{ChainID: n.params.ChainID, To: bob.Address, Amount: 1, Fee: 1, Nonce: 0}
			tx.Sign(n.creator.Private)
			return tx
		}, "fee_too_low"},
		{"сеть", func() *core.Tx {
			tx := &core.Tx{ChainID: "other", To: bob.Address, Amount: 1, Fee: n.params.MinFee}
			tx.Sign(n.creator.Private)
			return tx
		}, "wrong_chain"},
		{"ноль", func() *core.Tx { return n.tx(n.creator, bob.Address, 0, 0) }, "zero_amount"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := n.cl.SubmitTx(c.tx(), false, 0)
			if apiCode(err) != c.code {
				t.Fatalf("ожидался код %s, получено %v", c.code, err)
			}
		})
	}

	tx := n.tx(n.creator, bob.Address, 1, 0)
	if _, err := n.cl.SubmitTx(tx, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := n.cl.SubmitTx(tx, false, 0); apiCode(err) != "duplicate_tx" {
		t.Fatalf("повтор: %v", err)
	}
	n.produce()
	if _, err := n.cl.SubmitTx(tx, false, 0); apiCode(err) != "already_in_chain" {
		t.Fatalf("повтор после блока: %v", err)
	}

	for body, name := range map[string]string{
		"не json":                      "bad_request",
		`{"amount": 5}`:                "bad_request", // amount должен быть строкой
		`{"unknown_field": "x"}`:       "bad_request",
		`{"chain_id": "kovcheg-test"}`: "wrong_chain",
	} {
		resp, err := http.Post(n.srv.URL+"/v1/txs", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var e ErrorView
		json.Unmarshal(data, &e)
		if resp.StatusCode != 400 || (e.Code != name && !(name == "wrong_chain" && e.Code == "bad_address")) {
			t.Errorf("тело %q: %d %s", body, resp.StatusCode, data)
		}
	}

	big := bytes.Repeat([]byte("a"), maxBodySize+10)
	resp, _ := http.Post(n.srv.URL+"/v1/txs", "application/json", bytes.NewReader(big))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("слишком большое тело: %d", resp.StatusCode)
	}
}

func TestSubmitWait(t *testing.T) {
	n := newTestNode(t, nil)
	bob, _ := wallet.New()

	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				n.produce()
			}
		}
	}()
	res, err := n.cl.SubmitTx(n.tx(n.creator, bob.Address, 3, 0), true, 10*time.Second)
	close(stop)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != chain.TxConfirmed || res.Tx == nil || res.Tx.Block == nil || res.Tx.Confirmations < 1 {
		t.Fatalf("ожидалось подтверждение: %+v", res)
	}

	// Блоков нет — по таймауту возвращается pending.
	start := time.Now()
	res, err = n.cl.SubmitTx(n.tx(n.creator, bob.Address, 3, 1), true, time.Second)
	if err != nil || res.Status != chain.TxPending || time.Since(start) < time.Second {
		t.Fatalf("таймаут: %+v %v", res, err)
	}
}

func TestSubmitWaitDropped(t *testing.T) {
	n := newTestNode(t, nil)
	bob, carol := mustWallet(t), mustWallet(t)
	victim := n.tx(n.creator, bob.Address, 1, 0)

	done := make(chan error, 1)
	go func() {
		_, err := n.cl.SubmitTx(victim, true, 10*time.Second)
		done <- err
	}()
	// Ждём, пока транзакция окажется в мемпуле, и добавляем чужой блок с тем же nonce.
	for n.chain.MempoolSize() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	head := n.chain.Head()
	b := &core.Block{Height: 1, Time: head.Time + 5, PrevHash: head.Hash, Producer: n.creator.Address,
		Txs: []core.Tx{*n.tx(n.creator, carol.Address, 2, 0)}}
	b.Seal()
	if err := n.chain.AddBlock(b); err != nil {
		t.Fatal(err)
	}
	if err := <-done; apiCode(err) != "tx_dropped" {
		t.Fatalf("ожидалось tx_dropped, получено %v", err)
	}
}

func mustWallet(t *testing.T) *wallet.Wallet {
	w, err := wallet.New()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestTxLookup(t *testing.T) {
	n := newTestNode(t, nil)
	bob := mustWallet(t)
	res, _ := n.cl.SubmitTx(n.tx(n.creator, bob.Address, 7, 0), false, 0)

	tv, err := n.cl.Tx(res.Hash)
	if err != nil || tv.Status != chain.TxPending || tv.Block != nil || tv.AmountKVG != "7" || tv.Confirmations != 0 {
		t.Fatalf("pending: %+v %v", tv, err)
	}
	n.produce()
	n.produce() // пустой — не собирается
	tv, _ = n.cl.Tx(res.Hash)
	if tv.Status != chain.TxConfirmed || tv.Block.Height != 1 || tv.Block.Index != 0 || tv.Confirmations != 1 || tv.FeeKVG != "0.001" {
		t.Fatalf("confirmed: %+v", tv)
	}
	_, err = n.cl.Tx(core.Hash{1})
	if apiCode(err) != "not_found" {
		t.Fatalf("неизвестная: %v", err)
	}
	code, body := n.get("/v1/txs/zzz")
	wantError(t, code, body, 400, "bad_request")
}

func TestBlocks(t *testing.T) {
	n := newTestNode(t, func(o *Options) {})
	bob := mustWallet(t)
	for i := uint64(0); i < 5; i++ {
		n.cl.SubmitTx(n.tx(n.creator, bob.Address, 1, i), false, 0)
		n.produce()
	}
	// Высота 5. Постранично по 2.
	var heights []string
	before := ""
	for {
		l, err := n.cl.Blocks(before, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range l.Blocks {
			heights = append(heights, fmt.Sprint(b.Height))
		}
		if l.Next == "" {
			break
		}
		before = l.Next
	}
	if strings.Join(heights, " ") != "5 4 3 2 1 0" {
		t.Fatalf("блоки: %v", heights)
	}
	var l BlockList
	n.getJSON("/v1/blocks?before=100&limit=1", &l)
	if len(l.Blocks) != 1 || l.Blocks[0].Height != 5 {
		t.Fatalf("before больше высоты: %+v", l)
	}
	var l0 BlockList
	n.getJSON("/v1/blocks?before=0", &l0)
	if len(l0.Blocks) != 0 || l0.Next != "" {
		t.Fatalf("before=0: %+v", l0)
	}

	latest, err := n.cl.Block("latest")
	if err != nil || latest.Height != 5 || latest.TxCount != 1 || len(latest.Txs) != 1 || latest.Confirmations != 1 {
		t.Fatalf("latest: %+v %v", latest, err)
	}
	byHash, err := n.cl.Block(strings.ToUpper(latest.Hash.String()))
	if err != nil || byHash.Height != 5 {
		t.Fatalf("по хэшу: %+v %v", byHash, err)
	}
	g, _ := n.cl.Block("0")
	if g.Txs == nil || len(g.Txs) != 0 || g.Confirmations != 6 {
		t.Fatalf("генезис: %+v", g)
	}
	_, body := n.get("/v1/blocks/0")
	if !strings.Contains(string(body), `"txs": []`) {
		t.Fatalf("пустой список транзакций должен быть [], а не null: %s", body)
	}
	if _, err := n.cl.Block("99"); apiCode(err) != "not_found" {
		t.Fatalf("99: %v", err)
	}
	if _, err := n.cl.Block("abc"); apiCode(err) != "bad_request" {
		t.Fatalf("abc: %v", err)
	}
	if _, err := n.cl.Block(strings.Repeat("0", 64)); apiCode(err) != "not_found" {
		t.Fatalf("нулевой хэш: %v", err)
	}
}

func TestMempoolAndSearch(t *testing.T) {
	n := newTestNode(t, nil)
	bob := mustWallet(t)
	r1, _ := n.cl.SubmitTx(n.tx(n.creator, bob.Address, 1, 0), false, 0)
	n.produce()
	r2, _ := n.cl.SubmitTx(n.tx(n.creator, bob.Address, 1, 1), false, 0)
	n.cl.SubmitTx(n.tx(n.creator, bob.Address, 1, 2), false, 0)

	var mp MempoolView
	n.getJSON("/v1/mempool?limit=1", &mp)
	if mp.Total != 2 || len(mp.Txs) != 1 || mp.Txs[0].Hash != r2.Hash {
		t.Fatalf("мемпул: %+v", mp)
	}

	head := n.chain.Head()
	cases := map[string]SearchView{
		"1":                               {"block", "1"},
		" " + head.Hash.String():          {"block", "1"},
		r1.Hash.String():                  {"tx", r1.Hash.String()},
		strings.ToUpper(r2.Hash.String()): {"tx", r2.Hash.String()},
		string(bob.Address):               {"address", string(bob.Address)},
	}
	for q, want := range cases {
		var got SearchView
		n.getJSON("/v1/search?q="+strings.ReplaceAll(q, " ", "%20"), &got)
		if got != want {
			t.Errorf("поиск %q: %+v, ожидалось %+v", q, got, want)
		}
	}
	for _, q := range []string{"2", "kvg123", strings.Repeat("ab", 32), "hello"} {
		code, body := n.get("/v1/search?q=" + q)
		wantError(t, code, body, 404, "not_found")
	}
	code, body := n.get("/v1/search")
	wantError(t, code, body, 400, "bad_request")
}

// sseEvent — одно событие потока.
type sseEvent struct {
	id, event, data string
}

// readEvents читает события из потока в канал.
func readEvents(t *testing.T, body io.Reader) <-chan sseEvent {
	ch := make(chan sseEvent, 100)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.event != "" {
					ch <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				ev.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				ev.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[6:]
			}
		}
	}()
	return ch
}

func next(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("поток закрылся")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("событие не пришло")
	}
	return sseEvent{}
}

func openStream(t *testing.T, n *testNode, path string, header map[string]string) (<-chan sseEvent, func()) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, n.srv.URL+path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("поток: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	return readEvents(t, resp.Body), func() { resp.Body.Close() }
}

func TestEvents(t *testing.T) {
	n := newTestNode(t, nil)
	bob, carol := mustWallet(t), mustWallet(t)

	all, closeAll := openStream(t, n, "/v1/events", nil)
	defer closeAll()
	onlyBob, closeBob := openStream(t, n, "/v1/events?address="+string(bob.Address), nil)
	defer closeBob()
	time.Sleep(100 * time.Millisecond) // дать обработчикам подписаться

	n.cl.SubmitTx(n.tx(n.creator, carol.Address, 1, 0), false, 0)
	n.cl.SubmitTx(n.tx(n.creator, bob.Address, 2, 1), false, 0)
	n.produce()

	ev := next(t, all)
	var bs BlockSummary
	json.Unmarshal([]byte(ev.data), &bs)
	if ev.event != "block" || ev.id != "1" || bs.Height != 1 || bs.TxCount != 2 {
		t.Fatalf("событие блока: %+v", ev)
	}
	for i := 0; i < 2; i++ {
		if ev := next(t, all); ev.event != "tx" || ev.id != "1" {
			t.Fatalf("событие tx: %+v", ev)
		}
	}

	// Подписчик на адрес получает только свою транзакцию, с направлением.
	ev = next(t, onlyBob)
	var tv TxView
	json.Unmarshal([]byte(ev.data), &tv)
	if ev.event != "tx" || tv.To != bob.Address || tv.Direction != "in" || tv.AmountKVG != "2" || tv.Status != chain.TxConfirmed {
		t.Fatalf("событие для bob: %+v", ev)
	}

	// Догоняем пропущенное: Last-Event-ID=0 -> блок 1 придёт сразу, затем новые.
	n.cl.SubmitTx(n.tx(n.creator, carol.Address, 1, 2), false, 0)
	n.produce()
	replay, closeReplay := openStream(t, n, "/v1/events?types=block", map[string]string{"Last-Event-ID": "0"})
	defer closeReplay()
	if a, b := next(t, replay), next(t, replay); a.id != "1" || b.id != "2" || a.event != "block" {
		t.Fatalf("догон: %+v %+v", a, b)
	}
	n.cl.SubmitTx(n.tx(n.creator, carol.Address, 1, 3), false, 0)
	n.produce()
	if ev := next(t, replay); ev.id != "3" {
		t.Fatalf("после догона: %+v", ev)
	}

	code, body := n.get("/v1/events?types=xxx")
	wantError(t, code, body, 400, "bad_request")
	code, body = n.get("/v1/events?since=abc")
	wantError(t, code, body, 400, "bad_request")
}

func TestEventsLimit(t *testing.T) {
	n := newTestNode(t, func(o *Options) { o.MaxStreams = 1 })
	_, closeFirst := openStream(t, n, "/v1/events", nil)
	defer closeFirst()
	time.Sleep(50 * time.Millisecond)
	code, body := n.get("/v1/events")
	wantError(t, code, body, 503, "too_many_streams")
}

func TestReadOnly(t *testing.T) {
	n := newTestNode(t, func(o *Options) { o.ReadOnly = true })
	_, err := n.cl.SubmitTx(n.tx(n.creator, mustWallet(t).Address, 1, 0), false, 0)
	if apiCode(err) != "read_only" {
		t.Fatalf("ожидался read_only: %v", err)
	}
	if st, _ := n.cl.Status(); !st.ReadOnly {
		t.Fatal("статус не показывает read_only")
	}
}

func TestCORS(t *testing.T) {
	n := newTestNode(t, func(o *Options) { o.CORSOrigins = []string{"https://app.example"} })
	do := func(method, origin string) *http.Response {
		req, _ := http.NewRequest(method, n.srv.URL+"/v1/status", nil)
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := do(http.MethodGet, "https://app.example"); r.Header.Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatal("разрешённый Origin не получил CORS")
	}
	if r := do(http.MethodGet, "https://evil.example"); r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("чужой Origin получил CORS")
	}
	if r := do(http.MethodOptions, "https://app.example"); r.StatusCode != http.StatusNoContent ||
		!strings.Contains(r.Header.Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatalf("preflight: %d", r.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	n := newTestNode(t, func(o *Options) { o.RateLimit = 1; o.RateBurst = 3 })
	var codes []int
	for i := 0; i < 5; i++ {
		code, _ := n.get("/v1/status")
		codes = append(codes, code)
	}
	if fmt.Sprint(codes) != "[200 200 200 429 429]" {
		t.Fatalf("коды: %v", codes)
	}
	code, body := n.get("/v1/status")
	wantError(t, code, body, 429, "rate_limited")
}

func TestLimiterRefill(t *testing.T) {
	l := newLimiter(2, 2)
	t0 := time.Unix(1000, 0)
	if !l.allow("a", t0) || !l.allow("a", t0) || l.allow("a", t0) {
		t.Fatal("корзина на 2 запроса")
	}
	if !l.allow("b", t0) {
		t.Fatal("другой IP должен иметь свою корзину")
	}
	if !l.allow("a", t0.Add(500*time.Millisecond)) {
		t.Fatal("за 0.5 с при 2/с должен появиться токен")
	}
	l.allow("x", t0.Add(2*time.Minute)) // уборка
	if len(l.buckets) != 1 {
		t.Fatalf("старые корзины не убраны: %d", len(l.buckets))
	}
}

func TestTrustProxy(t *testing.T) {
	s := &server{opt: Options{TrustProxy: true}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4")
	if ip := s.clientIP(r); ip != "1.2.3.4" {
		t.Fatalf("IP %s", ip)
	}
	s.opt.TrustProxy = false
	if ip := s.clientIP(r); ip != "127.0.0.1" {
		t.Fatalf("без trust-proxy IP %s", ip)
	}
}

func TestLegacyEndpoints(t *testing.T) {
	n := newTestNode(t, nil)
	bob := mustWallet(t)

	var info InfoResponse
	n.getJSON("/info", &info)
	if info.ChainID != "kovcheg-test" || info.MinFee != 1_000_000 {
		t.Fatalf("/info: %+v", info)
	}
	tx := n.tx(n.creator, bob.Address, 4, 0)
	data, _ := json.Marshal(tx)
	resp, err := http.Post(n.srv.URL+"/tx", "application/json", bytes.NewReader(data))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("POST /tx: %v %v", resp.StatusCode, err)
	}
	var sub SubmitView
	json.NewDecoder(resp.Body).Decode(&sub)
	resp.Body.Close()
	n.produce()

	var tr TxResponse
	n.getJSON("/tx?hash="+sub.Hash.String(), &tr)
	if tr.Status != chain.TxConfirmed || tr.BlockHeight != 1 || tr.Tx.Hash != sub.Hash {
		t.Fatalf("/tx: %+v", tr)
	}
	var bal BalanceResponse
	n.getJSON("/balance?address="+string(bob.Address), &bal)
	if bal.BalanceKVG != "4" {
		t.Fatalf("/balance: %+v", bal)
	}
	var bv BlockView
	n.getJSON("/block?height=1", &bv)
	if bv.Height != 1 || len(bv.Txs) != 1 {
		t.Fatalf("/block: %+v", bv)
	}
	for path, code := range map[string]int{
		"/block?height=99":    404,
		"/block?height=x":     400,
		"/balance?address=xx": 400,
		"/nope":               404,
	} {
		if c, body := n.get(path); c != code {
			t.Errorf("%s: %d %s", path, c, body)
		}
	}
}

func TestMethodNotAllowed(t *testing.T) {
	n := newTestNode(t, nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodDelete, "/v1/status"},
		{http.MethodPost, "/v1/blocks/1"},
		{http.MethodGet, "/v1/txs"},
		{http.MethodPut, "/tx"},
	} {
		req, _ := http.NewRequest(c.method, n.srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		wantError(t, resp.StatusCode, body, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func TestNotFoundMessages(t *testing.T) {
	n := newTestNode(t, nil)
	_, err := n.cl.Tx(core.Hash{7})
	if err == nil || !strings.Contains(err.Error(), "транзакция не найдена") {
		t.Fatalf("tx: %v", err)
	}
	_, err = n.cl.Block("42")
	if err == nil || !strings.Contains(err.Error(), "блок не найден") {
		t.Fatalf("block: %v", err)
	}
}
