package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
)

// Адреса этапа 1: /info, /balance, /tx, /block. Формат прежний (новые поля только добавлены).
// Новым клиентам — /v1/...

// InfoResponse — ответ GET /info.
type InfoResponse struct {
	ChainID    string    `json:"chain_id"`
	MinFee     uint64    `json:"min_fee,string"`
	Height     uint64    `json:"height,string"`
	HeadHash   core.Hash `json:"head_hash"`
	MempoolTxs int       `json:"mempool_txs"`
}

// BalanceResponse — ответ GET /balance.
type BalanceResponse struct {
	Address      core.Address `json:"address"`
	Balance      uint64       `json:"balance,string"`
	BalanceKVG   string       `json:"balance_kvg"`
	Nonce        uint64       `json:"nonce,string"`
	PendingNonce uint64       `json:"pending_nonce,string"`
}

// TxResponse — ответ GET /tx.
type TxResponse struct {
	Status      chain.TxStatus `json:"status"`
	BlockHeight uint64         `json:"block_height,omitempty,string"`
	Tx          TxView         `json:"tx"`
}

func (s *server) legacyInfo(w http.ResponseWriter, r *http.Request) {
	head := s.c.Head()
	p := s.c.Params()
	writeJSON(w, http.StatusOK, InfoResponse{
		ChainID: p.ChainID, MinFee: p.MinFee, Height: head.Height,
		HeadHash: head.Hash, MempoolTxs: s.c.MempoolSize(),
	})
}

func (s *server) legacyBalance(w http.ResponseWriter, r *http.Request) {
	addr, err := core.ParseAddress(r.URL.Query().Get("address"))
	if err != nil {
		writeErr(w, err)
		return
	}
	v, err := s.accountView(addr)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, BalanceResponse{
		Address: v.Address, Balance: v.Balance, BalanceKVG: v.BalanceKVG,
		Nonce: v.Nonce, PendingNonce: v.PendingNonce,
	})
}

func (s *server) legacyGetTx(w http.ResponseWriter, r *http.Request) {
	h, err := core.ParseHash(r.URL.Query().Get("hash"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	info, err := s.c.Tx(h)
	if !txErr(w, err) {
		return
	}
	resp := TxResponse{Status: info.Status, Tx: s.txView(info, s.c.Head().Height)}
	if info.Status == chain.TxConfirmed {
		resp.BlockHeight = info.Height
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) legacyPostTx(w http.ResponseWriter, r *http.Request) {
	tx, err := decodeTx(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err)
		return
	}
	h, err := s.c.SubmitTx(tx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SubmitView{Hash: h, Status: chain.TxPending})
}

func (s *server) legacyBlock(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := "latest"
	switch {
	case q.Has("height"):
		if _, err := strconv.ParseUint(q.Get("height"), 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("неверный height"))
			return
		}
		id = q.Get("height")
	case q.Has("hash"):
		id = q.Get("hash")
	}
	b, err := s.findBlock(id)
	if !s.blockErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, s.blockView(b, s.c.Head().Height))
}
