// Package api — минимальный локальный HTTP-интерфейс узла и клиент к нему.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
)

// maxBodySize — предел размера тела POST /tx.
const maxBodySize = 64 << 10

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
	Balance      uint64       `json:"balance,string"` // нано-KVG, подтверждённый
	BalanceKVG   string       `json:"balance_kvg"`
	Nonce        uint64       `json:"nonce,string"`
	PendingNonce uint64       `json:"pending_nonce,string"` // nonce для следующей транзакции
}

// TxView — транзакция вместе с хэшем.
type TxView struct {
	Hash core.Hash `json:"hash"`
	core.Tx
}

// TxResponse — ответ GET /tx.
type TxResponse struct {
	Status      chain.TxStatus `json:"status"`
	BlockHeight uint64         `json:"block_height,omitempty,string"` // только для confirmed
	Tx          TxView         `json:"tx"`
}

// SubmitResponse — ответ POST /tx.
type SubmitResponse struct {
	Hash core.Hash `json:"hash"`
}

// BlockResponse — ответ GET /block: блок с хэшами транзакций.
type BlockResponse struct {
	Height   uint64       `json:"height,string"`
	Time     int64        `json:"time"`
	PrevHash core.Hash    `json:"prev_hash"`
	TxRoot   core.Hash    `json:"tx_root"`
	Producer core.Address `json:"producer"`
	Hash     core.Hash    `json:"hash"`
	Txs      []TxView     `json:"txs"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// NewHandler — HTTP-обработчик узла.
//
//	GET  /info                       — параметры сети и высота
//	GET  /balance?address=kvg...     — баланс и nonce
//	POST /tx                         — отправить подписанную транзакцию (JSON core.Tx)
//	GET  /tx?hash=...                — статус транзакции
//	GET  /block[?height=N|?hash=...] — блок (по умолчанию последний)
func NewHandler(c *chain.Chain) http.Handler {
	s := &server{c: c}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", s.info)
	mux.HandleFunc("GET /balance", s.balance)
	mux.HandleFunc("GET /tx", s.getTx)
	mux.HandleFunc("POST /tx", s.postTx)
	mux.HandleFunc("GET /block", s.block)
	return mux
}

type server struct{ c *chain.Chain }

func (s *server) info(w http.ResponseWriter, r *http.Request) {
	head := s.c.Head()
	p := s.c.Params()
	writeJSON(w, http.StatusOK, InfoResponse{
		ChainID:    p.ChainID,
		MinFee:     p.MinFee,
		Height:     head.Height,
		HeadHash:   head.Hash,
		MempoolTxs: s.c.MempoolSize(),
	})
}

func (s *server) balance(w http.ResponseWriter, r *http.Request) {
	addr, err := core.ParseAddress(r.URL.Query().Get("address"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	acc, err := s.c.Account(addr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	pend, err := s.c.PendingAccount(addr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, BalanceResponse{
		Address:      addr,
		Balance:      acc.Balance,
		BalanceKVG:   core.FormatAmount(acc.Balance),
		Nonce:        acc.Nonce,
		PendingNonce: pend.Nonce,
	})
}

func (s *server) getTx(w http.ResponseWriter, r *http.Request) {
	h, err := core.ParseHash(r.URL.Query().Get("hash"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tx, status, height, err := s.c.Tx(h)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("транзакция не найдена"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp := TxResponse{Status: status, Tx: TxView{Hash: h, Tx: *tx}}
	if status == chain.TxConfirmed {
		resp.BlockHeight = height
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) postTx(w http.ResponseWriter, r *http.Request) {
	var tx core.Tx
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tx); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("неверный JSON транзакции: "+err.Error()))
		return
	}
	h, err := s.c.SubmitTx(&tx)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, chain.ErrMempoolFull) {
			code = http.StatusServiceUnavailable
		}
		writeError(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, SubmitResponse{Hash: h})
}

func (s *server) block(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		b   *core.Block
		err error
	)
	switch {
	case q.Has("height"):
		h, perr := strconv.ParseUint(q.Get("height"), 10, 64)
		if perr != nil {
			writeError(w, http.StatusBadRequest, errors.New("неверный height"))
			return
		}
		b, err = s.c.BlockByHeight(h)
	case q.Has("hash"):
		h, perr := core.ParseHash(q.Get("hash"))
		if perr != nil {
			writeError(w, http.StatusBadRequest, perr)
			return
		}
		b, err = s.c.BlockByHash(h)
	default:
		b = s.c.Head()
	}
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("блок не найден"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, blockView(b))
}

func blockView(b *core.Block) BlockResponse {
	txs := make([]TxView, len(b.Txs))
	for i := range b.Txs {
		txs[i] = TxView{Hash: b.Txs[i].Hash(), Tx: b.Txs[i]}
	}
	return BlockResponse{
		Height:   b.Height,
		Time:     b.Time,
		PrevHash: b.PrevHash,
		TxRoot:   b.TxRoot,
		Producer: b.Producer,
		Hash:     b.Hash,
		Txs:      txs,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, errorResponse{Error: err.Error()})
}
