// Package chain — цепочка блоков одного узла: мемпул, сборка блоков, проверка и сохранение.
package chain

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
)

// Ошибки приёма транзакций и блоков.
var (
	ErrDuplicateTx    = errors.New("такая транзакция уже в мемпуле")
	ErrAlreadyInChain = errors.New("такая транзакция уже в блоке")
	ErrMempoolFull    = errors.New("мемпул переполнен")
	ErrBadBlock       = errors.New("неверный блок")
	ErrGenesisChanged = errors.New("база создана с другим genesis.json")
)

// Config — настройки узла.
type Config struct {
	Producer    core.Address // адрес, на который идут комиссии собранных блоков
	MaxBlockTxs int          // максимум транзакций в блоке
	MaxMempool  int          // максимум транзакций в мемпуле
	EmptyBlocks bool         // собирать блоки даже без транзакций
}

// DefaultConfig — настройки по умолчанию для указанного производителя.
func DefaultConfig(producer core.Address) Config {
	return Config{Producer: producer, MaxBlockTxs: 1000, MaxMempool: 10000}
}

// TxStatus — где сейчас транзакция.
type TxStatus string

const (
	TxPending   TxStatus = "pending"   // в мемпуле, ждёт блока
	TxConfirmed TxStatus = "confirmed" // в блоке
)

// Chain — цепочка узла. Все методы безопасны для вызова из разных горутин.
type Chain struct {
	mu      sync.Mutex
	store   *storage.Store
	params  core.Params
	cfg     Config
	head    *core.Block
	pool    []*core.Tx // в порядке приёма; у каждого отправителя nonce идут подряд
	poolIdx map[core.Hash]*core.Tx
	pending *core.Overlay // подтверждённое состояние + все транзакции мемпула
}

// New открывает цепочку. Если база пуста — записывает генезис-блок и начальные балансы,
// иначе проверяет, что база создана с этим же genesis.json.
func New(store *storage.Store, g *core.Genesis, cfg Config) (*Chain, error) {
	if err := cfg.Producer.Validate(); err != nil {
		return nil, fmt.Errorf("адрес производителя: %w", err)
	}
	if cfg.MaxBlockTxs <= 0 || cfg.MaxMempool <= 0 {
		return nil, errors.New("MaxBlockTxs и MaxMempool должны быть больше нуля")
	}
	gb, err := g.Block()
	if err != nil {
		return nil, err
	}

	height, ok, err := store.Head()
	if err != nil {
		return nil, err
	}
	if !ok {
		balances, _ := g.Balances()
		st := core.NewOverlay(nil)
		for addr, v := range balances {
			st.Set(addr, core.Account{Balance: v})
		}
		if err := store.CommitBlock(gb, st.Dirty()); err != nil {
			return nil, fmt.Errorf("запись генезиса: %w", err)
		}
		height = 0
	} else {
		gh, err := store.GenesisHash()
		if err != nil {
			return nil, err
		}
		if gh != gb.Hash {
			return nil, ErrGenesisChanged
		}
	}
	head, err := store.BlockByHeight(height)
	if err != nil {
		return nil, err
	}

	return &Chain{
		store:   store,
		params:  g.Params(),
		cfg:     cfg,
		head:    head,
		poolIdx: make(map[core.Hash]*core.Tx),
		pending: core.NewOverlay(store),
	}, nil
}

// Params — параметры сети.
func (c *Chain) Params() core.Params { return c.params }

// Head — последний блок.
func (c *Chain) Head() *core.Block {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.head
}

// Account — подтверждённое состояние аккаунта (по последнему блоку).
func (c *Chain) Account(addr core.Address) (core.Account, error) {
	return c.store.Account(addr)
}

// PendingAccount — состояние аккаунта с учётом транзакций в мемпуле.
// Его Nonce — это nonce для следующей транзакции отправителя.
func (c *Chain) PendingAccount(addr core.Address) (core.Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending.Account(addr)
}

// BlockByHeight читает блок по номеру.
func (c *Chain) BlockByHeight(h uint64) (*core.Block, error) { return c.store.BlockByHeight(h) }

// BlockByHash читает блок по хэшу.
func (c *Chain) BlockByHash(h core.Hash) (*core.Block, error) { return c.store.BlockByHash(h) }

// Tx ищет транзакцию в мемпуле и в блоках.
func (c *Chain) Tx(h core.Hash) (*core.Tx, TxStatus, uint64, error) {
	c.mu.Lock()
	if tx, ok := c.poolIdx[h]; ok {
		cp := *tx
		c.mu.Unlock()
		return &cp, TxPending, 0, nil
	}
	c.mu.Unlock()

	loc, err := c.store.TxLocation(h)
	if err != nil {
		return nil, "", 0, err
	}
	b, err := c.store.BlockByHeight(loc.Height)
	if err != nil {
		return nil, "", 0, err
	}
	if int(loc.Index) >= len(b.Txs) {
		return nil, "", 0, storage.ErrNotFound
	}
	return &b.Txs[loc.Index], TxConfirmed, loc.Height, nil
}

// MempoolSize — число транзакций в мемпуле.
func (c *Chain) MempoolSize() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pool)
}

// SubmitTx проверяет транзакцию (подпись, nonce, баланс с учётом мемпула) и кладёт её в мемпул.
func (c *Chain) SubmitTx(tx *core.Tx) (core.Hash, error) {
	cp := *tx
	h := cp.Hash()

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.poolIdx[h]; ok {
		return h, ErrDuplicateTx
	}
	if _, err := c.store.TxLocation(h); err == nil {
		return h, ErrAlreadyInChain
	} else if !errors.Is(err, storage.ErrNotFound) {
		return h, err
	}
	if len(c.pool) >= c.cfg.MaxMempool {
		return h, ErrMempoolFull
	}
	if err := core.ApplyTx(c.pending, &cp, c.params, c.cfg.Producer); err != nil {
		return h, err
	}
	c.pool = append(c.pool, &cp)
	c.poolIdx[h] = &cp
	return h, nil
}

// ProduceBlock собирает блок из мемпула и сохраняет его.
// Возвращает nil, если мемпул пуст и пустые блоки выключены.
func (c *Chain) ProduceBlock(now time.Time) (*core.Block, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.pool) == 0 && !c.cfg.EmptyBlocks {
		return nil, nil
	}

	st := core.NewOverlay(c.store)
	txs := make([]core.Tx, 0, min(len(c.pool), c.cfg.MaxBlockTxs))
	for _, tx := range c.pool {
		if len(txs) == c.cfg.MaxBlockTxs {
			break
		}
		// Невалидные к этому моменту транзакции просто не попадут в блок
		// и будут выброшены из мемпула в rebuildPool.
		if err := core.ApplyTx(st, tx, c.params, c.cfg.Producer); err == nil {
			txs = append(txs, *tx)
		}
	}

	t := now.Unix()
	if t < c.head.Time {
		t = c.head.Time
	}
	b := &core.Block{
		Height:   c.head.Height + 1,
		Time:     t,
		PrevHash: c.head.Hash,
		Producer: c.cfg.Producer,
		Txs:      txs,
	}
	b.Seal()
	if err := c.addBlockLocked(b); err != nil {
		return nil, err
	}
	return b, nil
}

// AddBlock полностью проверяет готовый блок и добавляет его в цепочку.
func (c *Chain) AddBlock(b *core.Block) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addBlockLocked(b)
}

func (c *Chain) addBlockLocked(b *core.Block) error {
	if b.Height != c.head.Height+1 {
		return fmt.Errorf("%w: номер %d, ожидался %d", ErrBadBlock, b.Height, c.head.Height+1)
	}
	if b.PrevHash != c.head.Hash {
		return fmt.Errorf("%w: хэш предыдущего блока не совпадает", ErrBadBlock)
	}
	if b.Time < c.head.Time {
		return fmt.Errorf("%w: время раньше предыдущего блока", ErrBadBlock)
	}
	if err := b.Producer.Validate(); err != nil {
		return fmt.Errorf("%w: производитель: %v", ErrBadBlock, err)
	}
	if len(b.Txs) > c.cfg.MaxBlockTxs {
		return fmt.Errorf("%w: слишком много транзакций", ErrBadBlock)
	}
	if core.TxRoot(b.Txs) != b.TxRoot {
		return fmt.Errorf("%w: tx_root не совпадает", ErrBadBlock)
	}
	if b.ComputeHash() != b.Hash {
		return fmt.Errorf("%w: хэш блока не совпадает", ErrBadBlock)
	}

	st := core.NewOverlay(c.store)
	for i := range b.Txs {
		if err := core.ApplyTx(st, &b.Txs[i], c.params, b.Producer); err != nil {
			return fmt.Errorf("%w: транзакция %d (%s): %v", ErrBadBlock, i, b.Txs[i].Hash(), err)
		}
	}
	if err := c.store.CommitBlock(b, st.Dirty()); err != nil {
		return err
	}
	c.head = b

	included := make(map[core.Hash]bool, len(b.Txs))
	for i := range b.Txs {
		included[b.Txs[i].Hash()] = true
	}
	c.rebuildPool(included)
	return nil
}

// rebuildPool убирает из мемпула попавшие в блок транзакции и те, что стали невалидными,
// и пересчитывает ожидаемое состояние.
func (c *Chain) rebuildPool(included map[core.Hash]bool) {
	old := c.pool
	c.pool = c.pool[:0:0]
	c.poolIdx = make(map[core.Hash]*core.Tx, len(old))
	c.pending = core.NewOverlay(c.store)
	for _, tx := range old {
		h := tx.Hash()
		if included[h] {
			continue
		}
		if err := core.ApplyTx(c.pending, tx, c.params, c.cfg.Producer); err != nil {
			continue
		}
		c.pool = append(c.pool, tx)
		c.poolIdx[h] = tx
	}
}

// Run собирает блок каждые interval, пока не отменён ctx.
func (c *Chain) Run(ctx context.Context, interval time.Duration, logger *log.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			b, err := c.ProduceBlock(now)
			if err != nil {
				logger.Printf("ошибка сборки блока: %v", err)
				continue
			}
			if b != nil {
				logger.Printf("блок #%d %s, транзакций: %d", b.Height, b.Hash, len(b.Txs))
			}
		}
	}
}
