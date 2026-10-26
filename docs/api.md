# HTTP API узла «Ковчег» (v1)

Адрес по умолчанию: `http://127.0.0.1:7070`. Все ответы — JSON в UTF-8.

## Общие правила

- **Суммы** — целые числа в нано-KVG (1 KVG = 1 000 000 000 нано-KVG),
  записанные **строкой**: `"amount": "12500000000"`. Так JavaScript не теряет
  точность (`Number` точен только до 2^53). Рядом для показа людям есть
  поля `*_kvg` (`"amount_kvg": "12.5"`) — считать по ним нельзя.
- Номера блоков, nonce, счётчики (`uint64`) — тоже строками.
- Хэши и ключи — hex, строчными буквами. Адрес: `kvg` + 40 hex-символов.
- Списки идут **от новых к старым**. Следующая страница — `?before=<next>`,
  где `next` взят из предыдущего ответа. Нет `next` — страниц больше нет.
  `?limit=` — от 1 до 100, по умолчанию 20.
- Пустой список — всегда `[]`, а не `null`.

### Ошибки

Любая ошибка — HTTP-статус не 200 и тело:

```json
{ "error": "недостаточно средств: баланс 1 KVG, нужно 2.001 KVG", "code": "insufficient_funds" }
```

`error` — текст для человека, `code` — для программы:

| code | HTTP | Что случилось |
|---|---|---|
| `bad_request` | 400 | неверный параметр или JSON |
| `bad_address` | 400 | адрес неверного формата |
| `bad_signature` | 400 | подпись не сходится |
| `from_mismatch` | 400 | `from` не соответствует `pub_key` |
| `bad_nonce` | 400 | nonce не равен ожидаемому (см. `pending_nonce`) |
| `insufficient_funds` | 400 | не хватает на `amount + fee` с учётом мемпула |
| `zero_amount` | 400 | сумма 0 |
| `fee_too_low` | 400 | комиссия меньше `min_fee` |
| `wrong_chain` | 400 | `chain_id` другой сети |
| `overflow` | 400 | переполнение суммы |
| `duplicate_tx` | 400 | такая транзакция уже в мемпуле |
| `already_in_chain` | 400 | такая транзакция уже в блоке |
| `not_found` | 404 | нет такого блока / транзакции / адреса API |
| `method_not_allowed` | 405 | метод не поддерживается |
| `tx_dropped` | 409 | (`?wait=true`) транзакция выброшена из мемпула |
| `read_only` | 403 | узел запущен с `-readonly` |
| `rate_limited` | 429 | слишком часто; повторить через `Retry-After` секунд |
| `mempool_full` | 503 | мемпул переполнен |
| `too_many_streams` | 503 | слишком много подключений к `/v1/events` |
| `internal` | 500 | ошибка узла |

## Запросы

### `GET /v1/status` — состояние сети

```json
{
  "version": "0.2.0",
  "chain_id": "kovcheg-local-1",
  "genesis_hash": "d2ac…",
  "min_fee": "1000000", "min_fee_kvg": "0.001",
  "height": "5", "head_hash": "7e90…", "head_time": 1793012632,
  "block_interval": 5,
  "tx_count": "6", "account_count": "2",
  "supply": "1000000000000000000", "supply_kvg": "1000000000",
  "mempool_txs": 0,
  "read_only": false
}
```

`head_time`, `time` везде — Unix-время в секундах (числом).

### `GET /v1/accounts/{address}` — аккаунт

```json
{
  "address": "kvg…",
  "balance": "15750000000", "balance_kvg": "15.75",
  "nonce": "0",
  "pending_balance": "15750000000", "pending_balance_kvg": "15.75",
  "pending_nonce": "0",
  "pending_txs": 0
}
```

- `balance`, `nonce` — по последнему блоку.
- `pending_*` — с учётом транзакций в мемпуле. **Для новой транзакции бери
  `pending_nonce`.**
- Неизвестный адрес — нули, не ошибка.

### `GET /v1/accounts/{address}/txs?limit=&before=` — история адреса

```json
{
  "pending": [ …TxView… ],
  "txs":     [ …TxView… ],
  "next": "2.0"
}
```

`pending` — транзакции адреса в мемпуле, только на первой странице (без `before`).
У каждой записи есть `direction`: `in`, `out` или `self` (перевод самому себе).

### `GET /v1/txs/{hash}` — транзакция (TxView)

```json
{
  "hash": "0fa3…",
  "chain_id": "kovcheg-local-1",
  "from": "kvg…", "to": "kvg…",
  "amount": "250000000", "fee": "1000000", "nonce": "5",
  "pub_key": "dde1…", "signature": "84ba…",
  "amount_kvg": "0.25", "fee_kvg": "0.001",
  "status": "confirmed",
  "block": { "height": "5", "hash": "7e90…", "time": 1793012632, "index": 0 },
  "confirmations": "1"
}
```

`status`: `pending` (в мемпуле, `block` нет, `confirmations` = "0") или `confirmed`.

### `POST /v1/txs?wait=&timeout=` — отправить транзакцию

Тело — подписанная транзакция (как собрать — ниже):

```json
{
  "chain_id": "kovcheg-local-1",
  "from": "kvg…", "to": "kvg…",
  "amount": "12500000000", "fee": "1000000", "nonce": "0",
  "pub_key": "<64 hex>", "signature": "<128 hex>"
}
```

Ответ: `{ "hash": "…", "status": "pending" }`.

С `?wait=true` узел ждёт блок (до `?timeout=` секунд, по умолчанию 30, максимум 60)
и отвечает `{ "hash", "status": "confirmed", "tx": TxView }`. Не дождался —
`"status": "pending"`. Транзакцию выбросили (её деньги потратили другой
транзакцией) — ошибка `tx_dropped`.

### `GET /v1/blocks?limit=&before=` — список блоков

```json
{ "blocks": [ …BlockSummary… ], "next": "4" }
```

BlockSummary: `height, hash, prev_hash, time, tx_root, producer, tx_count, confirmations`.

### `GET /v1/blocks/{id}` — блок

`id` — номер (`5`), хэш (64 hex) или `latest`. Ответ — BlockSummary + `"txs": [TxView…]`.
У генезиса (блок 0) `producer` пустой и транзакций нет.

### `GET /v1/mempool?limit=` — ожидающие транзакции

```json
{ "total": 2, "txs": [ …TxView… ] }
```

### `GET /v1/search?q=` — поиск

Понимает номер блока, адрес, хэш блока, хэш транзакции (в т.ч. в мемпуле):

```json
{ "type": "block", "id": "3" }
{ "type": "tx", "id": "<хэш>" }
{ "type": "address", "id": "kvg…" }
```

Ничего не нашлось — 404 `not_found`.

### `GET /v1/events` — события в реальном времени (Server-Sent Events)

Параметры:

- `?types=block,tx` — какие события слать. По умолчанию оба.
- `?address=kvg…` — только транзакции этого адреса (тогда по умолчанию `types=tx`).
- `?since=N` или заголовок `Last-Event-ID: N` — сначала прислать пропущенные блоки
  после N (не больше 1000 последних), потом новые.

```
retry: 3000

id: 5
event: block
data: {…BlockSummary…}

id: 5
event: tx
data: {…TxView…, "direction": "in"}

: ping
```

`id` — номер блока. Браузерный `EventSource` после обрыва сам переподключается
с `Last-Event-ID` и ничего не теряет. Каждые 15 с приходит `: ping`.

```js
const es = new EventSource("http://127.0.0.1:7070/v1/events?address=kvg…");
es.addEventListener("tx", (e) => {
  const tx = JSON.parse(e.data);
  console.log(tx.direction, tx.amount_kvg, "KVG");
});
```

## Как собрать и подписать транзакцию

Закрытый ключ **никогда** не отправляется на узел — подписывает клиент.

1. Ключ: ed25519. Из 32-байтного seed получается пара ключей.
2. Адрес: `"kvg" + hex(SHA-256(pub_key))[:40]` (первые 20 байт хэша).
3. Взять `chain_id` и `min_fee` из `/v1/status`, `pending_nonce` — из `/v1/accounts/{from}`.
4. Собрать **подписываемые байты** — подряд:

   | Поле | Кодирование |
   |---|---|
   | `"KVG-TX-v1"` | строка |
   | `chain_id` | строка |
   | `from` | строка |
   | `to` | строка |
   | `amount` | uint64, 8 байт big-endian |
   | `fee` | uint64, 8 байт big-endian |
   | `nonce` | uint64, 8 байт big-endian |
   | `pub_key` | байты (32) |

   «Строка» и «байты» = длина uint32 (4 байта big-endian) + сами байты (строки в UTF-8).

5. `signature` = ed25519-подпись этих байт (64 байта).
6. Хэш транзакции = SHA-256 подписываемых байт (подпись в хэш не входит).

Проверочные примеры: [`internal/core/testdata/sign_vectors.json`](../internal/core/testdata/sign_vectors.json) —
seed, адрес, транзакция, подписываемые байты, хэш и подпись. ed25519 детерминирован,
поэтому подпись должна совпасть байт в байт. Если у клиента совпали все векторы —
узел примет его транзакции.

## Старые адреса (этап 1)

Работают как раньше, для совместимости: `GET /info`, `GET /balance?address=`,
`GET /tx?hash=`, `POST /tx`, `GET /block?height=|hash=`. Новым клиентам — `/v1/…`.

## Настройки узла, влияющие на API

| Флаг | По умолчанию | |
|---|---|---|
| `-http` | `127.0.0.1:7070` | где слушать |
| `-cors` | пусто | Origin сайтов, которым браузер разрешит запросы: `https://a.ru,https://b.ru` или `*` |
| `-rate` | `20` | запросов в секунду с одного IP (разом можно вдвое больше); `0` — без ограничения |
| `-readonly` | выкл. | не принимать транзакции |
| `-trust-proxy` | выкл. | узел за туннелем/прокси: IP клиента — последний в `X-Forwarded-For` |

Без `-trust-proxy` за туннелем все запросы выглядят как пришедшие с 127.0.0.1 и делят
одно ограничение частоты. Включать `-trust-proxy` можно, только если узел доступен
**исключительно** через прокси — иначе клиент подставит любой IP.
