package core

import "fmt"

// Account — состояние аккаунта: баланс в нано-KVG и nonce —
// число уже принятых исходящих транзакций (следующая должна иметь именно этот nonce).
type Account struct {
	Balance uint64 `json:"balance,string"`
	Nonce   uint64 `json:"nonce,string"`
}

// Params — параметры сети, нужные для проверки транзакций.
type Params struct {
	ChainID string
	MinFee  uint64
}

// StateReader отдаёт состояние аккаунта. Неизвестный аккаунт — нулевой Account.
type StateReader interface {
	Account(addr Address) (Account, error)
}

// Overlay — изменения поверх базового состояния. Ничего не пишет в базу:
// изменения забираются через Dirty и сохраняются одной транзакцией вместе с блоком.
type Overlay struct {
	base  StateReader
	dirty map[Address]Account
}

// NewOverlay создаёт слой изменений поверх base (base может быть nil — пустое состояние).
func NewOverlay(base StateReader) *Overlay {
	return &Overlay{base: base, dirty: make(map[Address]Account)}
}

func (o *Overlay) Account(addr Address) (Account, error) {
	if a, ok := o.dirty[addr]; ok {
		return a, nil
	}
	if o.base == nil {
		return Account{}, nil
	}
	return o.base.Account(addr)
}

// Set записывает аккаунт в слой.
func (o *Overlay) Set(addr Address, a Account) { o.dirty[addr] = a }

// Dirty — все изменённые аккаунты.
func (o *Overlay) Dirty() map[Address]Account { return o.dirty }

// ApplyTx проверяет транзакцию против состояния и применяет её:
// списывает amount+fee и увеличивает nonce отправителя, зачисляет amount получателю,
// fee — производителю блока. При ошибке состояние не меняется.
func ApplyTx(st *Overlay, tx *Tx, p Params, producer Address) error {
	if err := tx.ValidateBasic(p); err != nil {
		return err
	}
	from, err := st.Account(tx.From)
	if err != nil {
		return err
	}
	if tx.Nonce != from.Nonce {
		return fmt.Errorf("%w: ожидался %d, получен %d", ErrBadNonce, from.Nonce, tx.Nonce)
	}
	cost := tx.Amount + tx.Fee // переполнение исключено в ValidateBasic
	if from.Balance < cost {
		return fmt.Errorf("%w: баланс %s KVG, нужно %s KVG",
			ErrInsufficientFunds, FormatAmount(from.Balance), FormatAmount(cost))
	}

	// Сначала проверяем все зачисления, потом пишем — чтобы ошибка не оставила полуприменённую транзакцию.
	from.Balance -= cost
	from.Nonce++
	credits := map[Address]uint64{tx.To: tx.Amount}
	if tx.Fee > 0 {
		credits[producer] += tx.Fee
	}
	updated := map[Address]Account{tx.From: from}
	for addr, amount := range credits {
		acc, ok := updated[addr]
		if !ok {
			if acc, err = st.Account(addr); err != nil {
				return err
			}
		}
		if acc.Balance > ^uint64(0)-amount {
			return ErrOverflow
		}
		acc.Balance += amount
		updated[addr] = acc
	}
	for addr, acc := range updated {
		st.Set(addr, acc)
	}
	return nil
}
