// Package storage хранит блоки и состояние аккаунтов в bbolt.
package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/makeluaurights/kovchegchain/internal/core"
)

var (
	bucketMeta     = []byte("meta")     // служебные значения
	bucketBlocks   = []byte("blocks")   // номер (8 байт BE) -> блок JSON
	bucketHashes   = []byte("hashes")   // хэш блока -> номер
	bucketTxs      = []byte("txs")      // хэш транзакции -> номер блока (8) + индекс (4)
	bucketAccounts = []byte("accounts") // адрес -> баланс (8) + nonce (8)
	bucketAddrTxs  = []byte("addrtxs")  // адрес + номер блока (8) + индекс (4) -> хэш транзакции

	keyHead         = []byte("head")          // номер последнего блока
	keyGenesis      = []byte("genesis")       // хэш генезис-блока
	keyTxCount      = []byte("tx_count")      // всего транзакций в блоках
	keyAccountCount = []byte("account_count") // всего аккаунтов
	keyIndexVersion = []byte("index_version") // версия индексов; при смене индексы перестраиваются
)

// indexVersion — текущая версия индексов. Базы этапа 1 не имели addrtxs и счётчиков.
const indexVersion = 2

// ErrNotFound — запрошенного объекта нет в базе.
var ErrNotFound = errors.New("не найдено")

// TxLocation — где лежит транзакция.
type TxLocation struct {
	Height uint64
	Index  uint32
}

// AddressTx — запись истории адреса.
type AddressTx struct {
	TxLocation
	Hash core.Hash
}

// Stats — счётчики базы.
type Stats struct {
	TxCount      uint64
	AccountCount uint64
}

// Store — база узла.
type Store struct {
	db *bolt.DB
}

// Open открывает (или создаёт) файл базы и при необходимости перестраивает индексы.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, fmt.Errorf("база %s занята: возможно, узел уже запущен", path)
		}
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketMeta, bucketBlocks, bucketHashes, bucketTxs, bucketAccounts, bucketAddrTxs} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return upgradeIndexes(tx)
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// upgradeIndexes перестраивает индексы, если база создана старой версией узла.
// Блоки и аккаунты не трогаются — индексы целиком выводятся из них.
func upgradeIndexes(tx *bolt.Tx) error {
	meta := tx.Bucket(bucketMeta)
	if v := meta.Get(keyIndexVersion); len(v) == 8 && binary.BigEndian.Uint64(v) == indexVersion {
		return nil
	}
	for _, name := range [][]byte{bucketTxs, bucketAddrTxs} {
		if err := tx.DeleteBucket(name); err != nil {
			return err
		}
		if _, err := tx.CreateBucket(name); err != nil {
			return err
		}
	}
	var txCount uint64
	err := tx.Bucket(bucketBlocks).ForEach(func(k, v []byte) error {
		var b core.Block
		if err := json.Unmarshal(v, &b); err != nil {
			return fmt.Errorf("блок %d повреждён: %w", binary.BigEndian.Uint64(k), err)
		}
		txCount += uint64(len(b.Txs))
		return indexBlockTxs(tx, &b)
	})
	if err != nil {
		return err
	}
	var accounts uint64
	if err := tx.Bucket(bucketAccounts).ForEach(func(_, _ []byte) error { accounts++; return nil }); err != nil {
		return err
	}
	if err := meta.Put(keyTxCount, u64(txCount)); err != nil {
		return err
	}
	if err := meta.Put(keyAccountCount, u64(accounts)); err != nil {
		return err
	}
	return meta.Put(keyIndexVersion, u64(indexVersion))
}

// indexBlockTxs пишет индексы транзакций блока: хэш -> место и историю обоих адресов.
// Перевод самому себе даёт один и тот же ключ — в истории он один раз.
func indexBlockTxs(tx *bolt.Tx, b *core.Block) error {
	txs := tx.Bucket(bucketTxs)
	addrs := tx.Bucket(bucketAddrTxs)
	for i := range b.Txs {
		h := b.Txs[i].Hash()
		loc := binary.BigEndian.AppendUint32(heightKey(b.Height), uint32(i))
		if err := txs.Put(h[:], loc); err != nil {
			return err
		}
		for _, a := range []core.Address{b.Txs[i].From, b.Txs[i].To} {
			if err := addrs.Put(addrTxKey(a, loc), h[:]); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close закрывает базу.
func (s *Store) Close() error { return s.db.Close() }

// Head возвращает номер последнего блока; ok=false, если база пуста.
func (s *Store) Head() (height uint64, ok bool, err error) {
	err = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketMeta).Get(keyHead)
		if v == nil {
			return nil
		}
		height, ok = binary.BigEndian.Uint64(v), true
		return nil
	})
	return
}

// GenesisHash — хэш генезис-блока, с которым создана база.
func (s *Store) GenesisHash() (core.Hash, error) {
	var h core.Hash
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketMeta).Get(keyGenesis)
		if v == nil {
			return ErrNotFound
		}
		copy(h[:], v)
		return nil
	})
	return h, err
}

// Stats — счётчики транзакций и аккаунтов.
func (s *Store) Stats() (Stats, error) {
	var st Stats
	err := s.db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		st.TxCount = getU64(meta, keyTxCount)
		st.AccountCount = getU64(meta, keyAccountCount)
		return nil
	})
	return st, err
}

// Account читает аккаунт. Неизвестный адрес — нулевой аккаунт.
func (s *Store) Account(addr core.Address) (core.Account, error) {
	var a core.Account
	err := s.db.View(func(tx *bolt.Tx) error {
		a = decodeAccount(tx.Bucket(bucketAccounts).Get([]byte(addr)))
		return nil
	})
	return a, err
}

// BlockByHeight читает блок по номеру.
func (s *Store) BlockByHeight(height uint64) (*core.Block, error) {
	var b *core.Block
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		b, err = getBlock(tx, height)
		return err
	})
	return b, err
}

// BlockByHash читает блок по хэшу.
func (s *Store) BlockByHash(h core.Hash) (*core.Block, error) {
	var b *core.Block
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketHashes).Get(h[:])
		if v == nil {
			return ErrNotFound
		}
		var err error
		b, err = getBlock(tx, binary.BigEndian.Uint64(v))
		return err
	})
	return b, err
}

// TxLocation ищет, в каком блоке лежит транзакция.
func (s *Store) TxLocation(h core.Hash) (TxLocation, error) {
	var loc TxLocation
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketTxs).Get(h[:])
		if v == nil {
			return ErrNotFound
		}
		loc = decodeLocation(v)
		return nil
	})
	return loc, err
}

// AddressTxs возвращает историю адреса от новых к старым: до limit записей строго раньше before
// (before == nil — с самой новой). more=true, если есть записи старше последней возвращённой.
func (s *Store) AddressTxs(addr core.Address, before *TxLocation, limit int) (out []AddressTx, more bool, err error) {
	prefix := []byte(addr)
	var start []byte
	if before != nil {
		start = addrTxKey(addr, binary.BigEndian.AppendUint32(heightKey(before.Height), before.Index))
	} else {
		start = append(append([]byte{}, prefix...), bytes.Repeat([]byte{0xff}, 12)...)
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketAddrTxs).Cursor()
		// Seek даёт первый ключ >= start; нам нужны ключи строго меньше start.
		k, v := c.Seek(start)
		if k == nil {
			k, v = c.Last()
		} else {
			k, v = c.Prev()
		}
		for ; k != nil && bytes.HasPrefix(k, prefix) && len(k) == len(prefix)+12; k, v = c.Prev() {
			if len(out) == limit {
				more = true
				return nil
			}
			var h core.Hash
			copy(h[:], v)
			out = append(out, AddressTx{TxLocation: decodeLocation(k[len(prefix):]), Hash: h})
		}
		return nil
	})
	return out, more, err
}

// CommitBlock атомарно сохраняет блок, индексы и изменённые аккаунты.
// Либо записывается всё, либо ничего — база не бывает в полузаписанном состоянии.
func (s *Store) CommitBlock(b *core.Block, accounts map[core.Address]core.Account) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket(bucketMeta)
		head := meta.Get(keyHead)
		switch {
		case head == nil && b.Height != 0:
			return fmt.Errorf("первым должен быть блок 0, а не %d", b.Height)
		case head != nil && binary.BigEndian.Uint64(head)+1 != b.Height:
			return fmt.Errorf("ожидался блок %d, получен %d", binary.BigEndian.Uint64(head)+1, b.Height)
		}

		hk := heightKey(b.Height)
		if err := tx.Bucket(bucketBlocks).Put(hk, data); err != nil {
			return err
		}
		if err := tx.Bucket(bucketHashes).Put(b.Hash[:], hk); err != nil {
			return err
		}
		if err := indexBlockTxs(tx, b); err != nil {
			return err
		}
		acc := tx.Bucket(bucketAccounts)
		var newAccounts uint64
		for addr, a := range accounts {
			if acc.Get([]byte(addr)) == nil {
				newAccounts++
			}
			if err := acc.Put([]byte(addr), encodeAccount(a)); err != nil {
				return err
			}
		}
		if err := meta.Put(keyTxCount, u64(getU64(meta, keyTxCount)+uint64(len(b.Txs)))); err != nil {
			return err
		}
		if err := meta.Put(keyAccountCount, u64(getU64(meta, keyAccountCount)+newAccounts)); err != nil {
			return err
		}
		if b.Height == 0 {
			if err := meta.Put(keyGenesis, b.Hash[:]); err != nil {
				return err
			}
		}
		return meta.Put(keyHead, hk)
	})
}

func getBlock(tx *bolt.Tx, height uint64) (*core.Block, error) {
	v := tx.Bucket(bucketBlocks).Get(heightKey(height))
	if v == nil {
		return nil, ErrNotFound
	}
	var b core.Block
	if err := json.Unmarshal(v, &b); err != nil {
		return nil, fmt.Errorf("блок %d повреждён: %w", height, err)
	}
	return &b, nil
}

func heightKey(h uint64) []byte { return binary.BigEndian.AppendUint64(nil, h) }

func addrTxKey(a core.Address, loc []byte) []byte {
	return append([]byte(a), loc...)
}

func decodeLocation(v []byte) TxLocation {
	return TxLocation{Height: binary.BigEndian.Uint64(v[:8]), Index: binary.BigEndian.Uint32(v[8:12])}
}

func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

func getU64(b *bolt.Bucket, key []byte) uint64 {
	v := b.Get(key)
	if len(v) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

func encodeAccount(a core.Account) []byte {
	b := binary.BigEndian.AppendUint64(nil, a.Balance)
	return binary.BigEndian.AppendUint64(b, a.Nonce)
}

func decodeAccount(v []byte) core.Account {
	if len(v) != 16 {
		return core.Account{}
	}
	return core.Account{
		Balance: binary.BigEndian.Uint64(v[:8]),
		Nonce:   binary.BigEndian.Uint64(v[8:]),
	}
}
