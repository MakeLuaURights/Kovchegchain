package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// GenesisAlloc — начальный баланс адреса. Amount — в KVG десятичной строкой ("1000000000").
type GenesisAlloc struct {
	Address Address `json:"address"`
	Amount  string  `json:"amount"`
}

// Genesis — содержимое genesis.json.
type Genesis struct {
	ChainID   string         `json:"chain_id"`
	Timestamp int64          `json:"timestamp"` // Unix-время в секундах
	MinFee    string         `json:"min_fee"`   // минимальная комиссия в KVG
	Alloc     []GenesisAlloc `json:"alloc"`
}

// LoadGenesis читает и проверяет genesis.json.
func LoadGenesis(path string) (*Genesis, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g Genesis
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("genesis.json: %w", err)
	}
	if err := g.Validate(); err != nil {
		return nil, fmt.Errorf("genesis.json: %w", err)
	}
	return &g, nil
}

// Validate проверяет поля генезиса.
func (g *Genesis) Validate() error {
	if g.ChainID == "" {
		return errors.New("пустой chain_id")
	}
	if g.Timestamp <= 0 {
		return errors.New("timestamp должен быть больше нуля")
	}
	if _, err := ParseAmount(g.MinFee); err != nil {
		return fmt.Errorf("min_fee: %w", err)
	}
	balances, err := g.Balances()
	if err != nil {
		return err
	}
	if len(balances) == 0 {
		return errors.New("alloc пуст: монеты некому выдать")
	}
	return nil
}

// Params — параметры сети из генезиса.
func (g *Genesis) Params() Params {
	fee, _ := ParseAmount(g.MinFee)
	return Params{ChainID: g.ChainID, MinFee: fee}
}

// Balances — начальные балансы в нано-KVG.
func (g *Genesis) Balances() (map[Address]uint64, error) {
	out := make(map[Address]uint64, len(g.Alloc))
	var total uint64
	for i, a := range g.Alloc {
		if err := a.Address.Validate(); err != nil {
			return nil, fmt.Errorf("alloc[%d]: %w", i, err)
		}
		if _, dup := out[a.Address]; dup {
			return nil, fmt.Errorf("alloc[%d]: адрес %s указан дважды", i, a.Address)
		}
		v, err := ParseAmount(a.Amount)
		if err != nil {
			return nil, fmt.Errorf("alloc[%d]: %w", i, err)
		}
		if total > ^uint64(0)-v {
			return nil, ErrOverflow
		}
		total += v
		out[a.Address] = v
	}
	return out, nil
}

// Block строит блок №0. Генезис-блок не содержит транзакций; его PrevHash —
// SHA-256 от канонической записи genesis.json, поэтому разные генезисы дают разные цепочки.
func (g *Genesis) Block() (*Block, error) {
	balances, err := g.Balances()
	if err != nil {
		return nil, err
	}
	addrs := make([]Address, 0, len(balances))
	for a := range balances {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i] < addrs[j] })

	p := g.Params()
	e := encoder{}
	e.str("KVG-GENESIS-v1")
	e.str(g.ChainID)
	e.i64(g.Timestamp)
	e.u64(p.MinFee)
	e.u64(uint64(len(addrs)))
	for _, a := range addrs {
		e.str(string(a))
		e.u64(balances[a])
	}

	b := &Block{Height: 0, Time: g.Timestamp, PrevHash: e.sum(), Txs: []Tx{}}
	b.Seal()
	return b, nil
}
