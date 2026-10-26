// Package storage хранит блоки и состояние аккаунтов в bbolt.
package storage

import (
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

	keyHead    = []byte("head")    // номер последнего блока
	keyGenesis = []byte("genesis") // хэш генезис-блока
)

// ErrNotFound — запрошенного объекта нет в базе.
var ErrNotFound = errors.New("не найдено")

// TxLocation — где лежит транзакция.
type TxLocation struct {
	Height uint64
	Index  uint32
}

// Store — база узла.
type Store struct {
	db *bolt.DB
}

// Open открывает (или создаёт) файл базы.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, fmt.Errorf("база %s занята: возможно, узел уже запущен", path)
		}
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketMeta, bucketBlocks, bucketHashes, bucketTxs, bucketAccounts} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
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
		loc.Height = binary.BigEndian.Uint64(v[:8])
		loc.Index = binary.BigEndian.Uint32(v[8:12])
		return nil
	})
	return loc, err
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
		txs := tx.Bucket(bucketTxs)
		for i := range b.Txs {
			h := b.Txs[i].Hash()
			loc := binary.BigEndian.AppendUint32(heightKey(b.Height), uint32(i))
			if err := txs.Put(h[:], loc); err != nil {
				return err
			}
		}
		acc := tx.Bucket(bucketAccounts)
		for addr, a := range accounts {
			if err := acc.Put([]byte(addr), encodeAccount(a)); err != nil {
				return err
			}
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
