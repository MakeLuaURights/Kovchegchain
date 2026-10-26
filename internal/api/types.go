package api

import (
	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
)

// Все uint64 в JSON — строками (`,string`): JavaScript не теряет точность.
// Поля *_kvg — те же суммы в KVG для показа людям; считать по ним нельзя.

// StatusView — ответ GET /v1/status.
type StatusView struct {
	Version       string    `json:"version"`
	ChainID       string    `json:"chain_id"`
	GenesisHash   core.Hash `json:"genesis_hash"`
	MinFee        uint64    `json:"min_fee,string"`
	MinFeeKVG     string    `json:"min_fee_kvg"`
	Height        uint64    `json:"height,string"`
	HeadHash      core.Hash `json:"head_hash"`
	HeadTime      int64     `json:"head_time"`
	BlockInterval int64     `json:"block_interval"` // секунды
	TxCount       uint64    `json:"tx_count,string"`
	AccountCount  uint64    `json:"account_count,string"`
	Supply        uint64    `json:"supply,string"`
	SupplyKVG     string    `json:"supply_kvg"`
	MempoolTxs    int       `json:"mempool_txs"`
	ReadOnly      bool      `json:"read_only"`
}

// AccountView — ответ GET /v1/accounts/{address}.
type AccountView struct {
	Address           core.Address `json:"address"`
	Balance           uint64       `json:"balance,string"` // подтверждённый, нано-KVG
	BalanceKVG        string       `json:"balance_kvg"`
	Nonce             uint64       `json:"nonce,string"`
	PendingBalance    uint64       `json:"pending_balance,string"` // с учётом мемпула
	PendingBalanceKVG string       `json:"pending_balance_kvg"`
	PendingNonce      uint64       `json:"pending_nonce,string"` // nonce для следующей транзакции
	PendingTxs        int          `json:"pending_txs"`
}

// TxBlockRef — где лежит подтверждённая транзакция.
type TxBlockRef struct {
	Height uint64    `json:"height,string"`
	Hash   core.Hash `json:"hash"`
	Time   int64     `json:"time"`
	Index  uint32    `json:"index"`
}

// TxView — транзакция для API.
type TxView struct {
	Hash core.Hash `json:"hash"`
	core.Tx
	AmountKVG     string         `json:"amount_kvg"`
	FeeKVG        string         `json:"fee_kvg"`
	Status        chain.TxStatus `json:"status"`
	Block         *TxBlockRef    `json:"block,omitempty"` // только для confirmed
	Confirmations uint64         `json:"confirmations,string"`
	Direction     string         `json:"direction,omitempty"` // в истории адреса: in, out, self
}

// TxList — ответ GET /v1/accounts/{address}/txs.
type TxList struct {
	Pending []TxView `json:"pending"` // только на первой странице
	Txs     []TxView `json:"txs"`
	Next    string   `json:"next,omitempty"` // значение для ?before= следующей страницы
}

// MempoolView — ответ GET /v1/mempool.
type MempoolView struct {
	Total int      `json:"total"`
	Txs   []TxView `json:"txs"`
}

// BlockSummary — блок без транзакций.
type BlockSummary struct {
	Height        uint64       `json:"height,string"`
	Hash          core.Hash    `json:"hash"`
	PrevHash      core.Hash    `json:"prev_hash"`
	Time          int64        `json:"time"`
	TxRoot        core.Hash    `json:"tx_root"`
	Producer      core.Address `json:"producer"`
	TxCount       int          `json:"tx_count"`
	Confirmations uint64       `json:"confirmations,string"`
}

// BlockView — блок с транзакциями.
type BlockView struct {
	BlockSummary
	Txs []TxView `json:"txs"`
}

// BlockList — ответ GET /v1/blocks.
type BlockList struct {
	Blocks []BlockSummary `json:"blocks"`
	Next   string         `json:"next,omitempty"` // значение для ?before= следующей страницы
}

// SubmitView — ответ POST /v1/txs.
type SubmitView struct {
	Hash   core.Hash      `json:"hash"`
	Status chain.TxStatus `json:"status"`
	Tx     *TxView        `json:"tx,omitempty"` // с ?wait=true, если транзакция подтверждена
}

// SearchView — ответ GET /v1/search.
type SearchView struct {
	Type string `json:"type"` // block, tx, address
	ID   string `json:"id"`   // номер блока, хэш транзакции или адрес
}

// ErrorView — тело любого ответа с ошибкой.
type ErrorView struct {
	Error string `json:"error"` // текст для человека
	Code  string `json:"code"`  // код для программы, см. docs/api.md
}
