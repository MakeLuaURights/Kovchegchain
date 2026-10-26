package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
)

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.c.Stats()
	if err != nil {
		writeErr(w, err)
		return
	}
	head := s.c.Head()
	p := s.c.Params()
	writeJSON(w, http.StatusOK, StatusView{
		Version:       s.opt.Version,
		ChainID:       p.ChainID,
		GenesisHash:   s.c.GenesisHash(),
		MinFee:        p.MinFee,
		MinFeeKVG:     core.FormatAmount(p.MinFee),
		Height:        head.Height,
		HeadHash:      head.Hash,
		HeadTime:      head.Time,
		BlockInterval: int64(s.opt.BlockInterval / time.Second),
		TxCount:       st.TxCount,
		AccountCount:  st.AccountCount,
		Supply:        st.Supply,
		SupplyKVG:     core.FormatAmount(st.Supply),
		MempoolTxs:    st.MempoolSize,
		ReadOnly:      s.opt.ReadOnly,
	})
}

func (s *server) accountView(addr core.Address) (AccountView, error) {
	acc, err := s.c.Account(addr)
	if err != nil {
		return AccountView{}, err
	}
	pend, err := s.c.PendingAccount(addr)
	if err != nil {
		return AccountView{}, err
	}
	return AccountView{
		Address:           addr,
		Balance:           acc.Balance,
		BalanceKVG:        core.FormatAmount(acc.Balance),
		Nonce:             acc.Nonce,
		PendingBalance:    pend.Balance,
		PendingBalanceKVG: core.FormatAmount(pend.Balance),
		PendingNonce:      pend.Nonce,
		PendingTxs:        len(s.c.PendingFor(addr)),
	}, nil
}

func (s *server) account(w http.ResponseWriter, r *http.Request) {
	addr, err := core.ParseAddress(r.PathValue("address"))
	if err != nil {
		writeErr(w, err)
		return
	}
	v, err := s.accountView(addr)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// parseTxCursor разбирает курсор истории "номер_блока.индекс".
func parseTxCursor(s string) (*storage.TxLocation, error) {
	hs, is, ok := strings.Cut(s, ".")
	h, err1 := strconv.ParseUint(hs, 10, 64)
	i, err2 := strconv.ParseUint(is, 10, 32)
	if !ok || err1 != nil || err2 != nil {
		return nil, errors.New("неверный before: ожидается вид 12.0")
	}
	return &storage.TxLocation{Height: h, Index: uint32(i)}, nil
}

func (s *server) accountTxs(w http.ResponseWriter, r *http.Request) {
	addr, err := core.ParseAddress(r.PathValue("address"))
	if err != nil {
		writeErr(w, err)
		return
	}
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	var before *storage.TxLocation
	if b := r.URL.Query().Get("before"); b != "" {
		if before, err = parseTxCursor(b); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err)
			return
		}
	}

	head := s.c.Head().Height
	resp := TxList{Pending: []TxView{}, Txs: []TxView{}}
	if before == nil {
		for _, info := range s.c.PendingFor(addr) {
			v := s.txView(info, head)
			v.Direction = direction(&info.Tx, addr)
			resp.Pending = append(resp.Pending, v)
		}
	}
	infos, more, err := s.c.AddressTxs(addr, before, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, info := range infos {
		v := s.txView(info, head)
		v.Direction = direction(&info.Tx, addr)
		resp.Txs = append(resp.Txs, v)
	}
	if more && len(infos) > 0 {
		last := infos[len(infos)-1]
		resp.Next = fmt.Sprintf("%d.%d", last.Height, last.Index)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) blocks(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	head := s.c.Head().Height
	// Блоки идут от новых к старым; before — номер, строго ниже которого начинать.
	top := head
	if b := r.URL.Query().Get("before"); b != "" {
		n, err := strconv.ParseUint(b, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("неверный before"))
			return
		}
		if n == 0 {
			writeJSON(w, http.StatusOK, BlockList{Blocks: []BlockSummary{}})
			return
		}
		top = min(n-1, head)
	}

	resp := BlockList{Blocks: []BlockSummary{}}
	h := top
	for len(resp.Blocks) < limit {
		b, err := s.c.BlockByHeight(h)
		if err != nil {
			writeErr(w, err)
			return
		}
		resp.Blocks = append(resp.Blocks, summary(b, head))
		if h == 0 {
			break
		}
		h--
	}
	if last := resp.Blocks[len(resp.Blocks)-1]; last.Height > 0 {
		resp.Next = strconv.FormatUint(last.Height, 10)
	}
	writeJSON(w, http.StatusOK, resp)
}

// findBlock ищет блок по номеру, хэшу или "latest".
func (s *server) findBlock(id string) (*core.Block, error) {
	switch {
	case id == "latest":
		return s.c.Head(), nil
	case isDigits(id) && len(id) != hashHexLen:
		h, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			return nil, storage.ErrNotFound
		}
		return s.c.BlockByHeight(h)
	default:
		h, err := core.ParseHash(strings.ToLower(id))
		if err != nil {
			return nil, errBadID
		}
		return s.c.BlockByHash(h)
	}
}

// blockErr отвечает ошибкой поиска блока; true — ошибки нет.
func (s *server) blockErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, errBadID):
		writeError(w, http.StatusBadRequest, "bad_request", err)
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", errors.New("блок не найден"))
	default:
		writeErr(w, err)
	}
	return false
}

// txErr отвечает ошибкой поиска транзакции; true — ошибки нет.
func txErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", errors.New("транзакция не найдена"))
	default:
		writeErr(w, err)
	}
	return false
}

var errBadID = errors.New("ожидается номер блока, его хэш или latest")

// hashHexLen — длина хэша в hex. Строка такой длины — всегда хэш, даже если в ней одни цифры.
const hashHexLen = 64

func (s *server) blockV1(w http.ResponseWriter, r *http.Request) {
	b, err := s.findBlock(r.PathValue("id"))
	if !s.blockErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, s.blockView(b, s.c.Head().Height))
}

func (s *server) txV1(w http.ResponseWriter, r *http.Request) {
	h, err := core.ParseHash(strings.ToLower(r.PathValue("hash")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	info, err := s.c.Tx(h)
	if !txErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, s.txView(info, s.c.Head().Height))
}

// submitV1 принимает подписанную транзакцию. С ?wait=true ждёт её попадания в блок
// (не дольше ?timeout= секунд, по умолчанию 30, максимум Options.WaitTimeout).
func (s *server) submitV1(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wait := q.Get("wait") == "true" || q.Get("wait") == "1"
	timeout := 30 * time.Second
	if t := q.Get("timeout"); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("timeout — целое число секунд от 1"))
			return
		}
		timeout = time.Duration(n) * time.Second
	}
	timeout = min(timeout, s.opt.WaitTimeout)

	tx, err := decodeTx(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}

	// Подписываемся до отправки, чтобы не пропустить блок с этой транзакцией.
	var blocks <-chan *core.Block
	if wait {
		ch, cancel := s.c.Subscribe()
		defer cancel()
		blocks = ch
	}
	h, err := s.c.SubmitTx(tx)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !wait {
		writeJSON(w, http.StatusOK, SubmitView{Hash: h, Status: chain.TxPending})
		return
	}

	// Ответ может задержаться дольше стандартного WriteTimeout сервера.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + 10*time.Second))
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second) // на случай, если подписку отключили
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeJSON(w, http.StatusOK, SubmitView{Hash: h, Status: chain.TxPending})
			return
		case _, ok := <-blocks:
			if !ok {
				blocks = nil
			}
		case <-tick.C:
		}
		info, err := s.c.Tx(h)
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusConflict, "tx_dropped",
				errors.New("транзакция выброшена из мемпула: она стала невалидной"))
			return
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		if info.Status == chain.TxConfirmed {
			v := s.txView(info, s.c.Head().Height)
			writeJSON(w, http.StatusOK, SubmitView{Hash: h, Status: info.Status, Tx: &v})
			return
		}
	}
}

func (s *server) mempool(w http.ResponseWriter, r *http.Request) {
	limit, err := parseLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	resp := MempoolView{Total: s.c.MempoolSize(), Txs: []TxView{}}
	for _, info := range s.c.Mempool(limit) {
		resp.Txs = append(resp.Txs, s.txView(info, 0))
	}
	writeJSON(w, http.StatusOK, resp)
}

// search определяет, что ввёл человек: номер блока, адрес, хэш блока или транзакции.
func (s *server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	notFound := func() {
		writeError(w, http.StatusNotFound, "not_found", errors.New("ничего не найдено"))
	}
	switch {
	case q == "":
		writeError(w, http.StatusBadRequest, "bad_request", errors.New("пустой запрос q"))
	case isDigits(q) && len(q) != hashHexLen:
		h, err := strconv.ParseUint(q, 10, 64)
		if err != nil || h > s.c.Head().Height {
			notFound()
			return
		}
		writeJSON(w, http.StatusOK, SearchView{Type: "block", ID: q})
	case strings.HasPrefix(q, core.AddressPrefix):
		if core.Address(q).Validate() != nil {
			notFound()
			return
		}
		writeJSON(w, http.StatusOK, SearchView{Type: "address", ID: q})
	default:
		h, err := core.ParseHash(q)
		if err != nil {
			notFound()
			return
		}
		if b, err := s.c.BlockByHash(h); err == nil {
			writeJSON(w, http.StatusOK, SearchView{Type: "block", ID: strconv.FormatUint(b.Height, 10)})
			return
		}
		if _, err := s.c.Tx(h); err == nil {
			writeJSON(w, http.StatusOK, SearchView{Type: "tx", ID: q})
			return
		}
		notFound()
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
