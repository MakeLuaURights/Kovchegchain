package core

// Block — блок цепочки. Hash считается от заголовка (номер, время, хэш предыдущего,
// корень транзакций, производитель), транзакции входят в него через TxRoot.
type Block struct {
	Height   uint64  `json:"height,string"`
	Time     int64   `json:"time"` // Unix-время в секундах
	PrevHash Hash    `json:"prev_hash"`
	TxRoot   Hash    `json:"tx_root"`
	Producer Address `json:"producer"` // кому идут комиссии блока
	Txs      []Tx    `json:"txs"`
	Hash     Hash    `json:"hash"`
}

// ComputeHash считает хэш заголовка блока.
func (b *Block) ComputeHash() Hash {
	e := encoder{}
	e.str("KVG-BLOCK-v1")
	e.u64(b.Height)
	e.i64(b.Time)
	e.hash(b.PrevHash)
	e.hash(b.TxRoot)
	e.str(string(b.Producer))
	return e.sum()
}

// Seal вычисляет TxRoot и Hash.
func (b *Block) Seal() {
	b.TxRoot = TxRoot(b.Txs)
	b.Hash = b.ComputeHash()
}

// TxRoot — корень дерева Меркла из хэшей транзакций.
// Для пустого списка — нулевой хэш; при нечётном числе узлов последний дублируется.
func TxRoot(txs []Tx) Hash {
	if len(txs) == 0 {
		return Hash{}
	}
	level := make([]Hash, len(txs))
	for i := range txs {
		level[i] = txs[i].Hash()
	}
	for len(level) > 1 {
		if len(level)%2 == 1 {
			level = append(level, level[len(level)-1])
		}
		next := make([]Hash, len(level)/2)
		for i := range next {
			e := encoder{}
			e.hash(level[2*i])
			e.hash(level[2*i+1])
			next[i] = e.sum()
		}
		level = next
	}
	return level[0]
}
