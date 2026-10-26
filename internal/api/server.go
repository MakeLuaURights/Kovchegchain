// Package api — HTTP-интерфейс узла и клиент к нему. Описание: docs/api.md.
package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
)

const (
	maxBodySize  = 64 << 10 // предел тела POST
	defaultLimit = 20       // размер страницы по умолчанию
	maxLimit     = 100      // максимальный размер страницы
)

// Options — настройки API.
type Options struct {
	Version       string
	BlockInterval time.Duration
	ReadOnly      bool     // запретить отправку транзакций
	CORSOrigins   []string // разрешённые Origin для браузеров; "*" — любой
	RateLimit     float64  // запросов в секунду с одного IP; 0 — без ограничения
	RateBurst     int      // сколько запросов можно сделать разом
	TrustProxy    bool     // брать IP клиента из X-Forwarded-For (узел за туннелем/прокси)
	MaxStreams    int      // максимум одновременных /v1/events; 0 — 100
	WaitTimeout   time.Duration
}

// DefaultOptions — настройки по умолчанию.
func DefaultOptions() Options {
	return Options{
		Version:       "dev",
		BlockInterval: 5 * time.Second,
		RateLimit:     20,
		RateBurst:     40,
		MaxStreams:    100,
		WaitTimeout:   60 * time.Second,
	}
}

type server struct {
	c       *chain.Chain
	opt     Options
	limiter *limiter
	streams chan struct{} // семафор для /v1/events
}

// NewHandler — HTTP-обработчик узла: API v1 и старые адреса этапа 1.
func NewHandler(c *chain.Chain, opt Options) http.Handler {
	if opt.MaxStreams <= 0 {
		opt.MaxStreams = 100
	}
	if opt.WaitTimeout <= 0 {
		opt.WaitTimeout = 60 * time.Second
	}
	s := &server{c: c, opt: opt, streams: make(chan struct{}, opt.MaxStreams)}
	if opt.RateLimit > 0 {
		burst := opt.RateBurst
		if burst < 1 {
			burst = 1
		}
		s.limiter = newLimiter(opt.RateLimit, burst)
	}

	mux := http.NewServeMux()
	// Для каждого пути, кроме обработчика метода, регистрируется запасной без метода:
	// он отвечает 405 в JSON, а не текстом по умолчанию.
	seen := map[string]bool{}
	handle := func(method, path string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+path, h)
		if !seen[path] {
			seen[path] = true
			mux.HandleFunc(path, methodNotAllowed)
		}
	}
	handle("GET", "/v1/status", s.status)
	handle("GET", "/v1/accounts/{address}", s.account)
	handle("GET", "/v1/accounts/{address}/txs", s.accountTxs)
	handle("GET", "/v1/blocks", s.blocks)
	handle("GET", "/v1/blocks/{id}", s.blockV1)
	handle("GET", "/v1/txs/{hash}", s.txV1)
	handle("POST", "/v1/txs", s.submitV1)
	handle("GET", "/v1/mempool", s.mempool)
	handle("GET", "/v1/search", s.search)
	handle("GET", "/v1/events", s.events)

	// Адреса этапа 1 — оставлены для совместимости.
	handle("GET", "/info", s.legacyInfo)
	handle("GET", "/balance", s.legacyBalance)
	handle("GET", "/tx", s.legacyGetTx)
	handle("POST", "/tx", s.legacyPostTx)
	handle("GET", "/block", s.legacyBlock)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", errors.New("нет такого адреса API"))
	})

	return s.middleware(mux)
}

// middleware: CORS, ограничение частоты, режим только чтения.
func (s *server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && s.corsAllowed(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if s.limiter != nil && !s.limiter.allow(s.clientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", errors.New("слишком много запросов, подождите"))
			return
		}
		if s.opt.ReadOnly && r.Method == http.MethodPost {
			writeError(w, http.StatusForbidden, "read_only", errors.New("узел работает только на чтение"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", errors.New("метод "+r.Method+" здесь не поддерживается"))
}

func (s *server) corsAllowed(origin string) bool {
	for _, o := range s.opt.CORSOrigins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

func (s *server) clientIP(r *http.Request) string {
	if s.opt.TrustProxy {
		// Последний адрес дописал наш прокси; первые клиент может подставить сам.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// errorCode сопоставляет ошибку узла с HTTP-статусом и кодом для программ.
func errorCode(err error) (int, string) {
	codes := []struct {
		err  error
		code string
	}{
		{core.ErrBadSignature, "bad_signature"},
		{core.ErrFromMismatch, "from_mismatch"},
		{core.ErrBadNonce, "bad_nonce"},
		{core.ErrInsufficientFunds, "insufficient_funds"},
		{core.ErrZeroAmount, "zero_amount"},
		{core.ErrFeeTooLow, "fee_too_low"},
		{core.ErrWrongChain, "wrong_chain"},
		{core.ErrOverflow, "overflow"},
		{core.ErrBadAddress, "bad_address"},
		{chain.ErrDuplicateTx, "duplicate_tx"},
		{chain.ErrAlreadyInChain, "already_in_chain"},
	}
	for _, c := range codes {
		if errors.Is(err, c.err) {
			return http.StatusBadRequest, c.code
		}
	}
	switch {
	case errors.Is(err, chain.ErrMempoolFull):
		return http.StatusServiceUnavailable, "mempool_full"
	case errors.Is(err, storage.ErrNotFound):
		return http.StatusNotFound, "not_found"
	}
	return http.StatusInternalServerError, "internal"
}

func writeErr(w http.ResponseWriter, err error) {
	code, name := errorCode(err)
	writeError(w, code, name, err)
}

func writeError(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, ErrorView{Error: err.Error(), Code: code})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// parseLimit читает ?limit= (по умолчанию defaultLimit, не больше maxLimit).
func parseLimit(r *http.Request) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, errors.New("limit должен быть целым числом от 1")
	}
	return min(n, maxLimit), nil
}

// decodeTx читает подписанную транзакцию из тела запроса.
func decodeTx(w http.ResponseWriter, r *http.Request) (*core.Tx, error) {
	var tx core.Tx
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodySize))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tx); err != nil {
		return nil, errors.New("неверный JSON транзакции: " + err.Error())
	}
	return &tx, nil
}

// --- построение ответов ---

func (s *server) txView(info chain.TxInfo, headHeight uint64) TxView {
	v := TxView{
		Hash:      info.Hash,
		Tx:        info.Tx,
		AmountKVG: core.FormatAmount(info.Tx.Amount),
		FeeKVG:    core.FormatAmount(info.Tx.Fee),
		Status:    info.Status,
	}
	if info.Status == chain.TxConfirmed {
		v.Block = &TxBlockRef{Height: info.Height, Hash: info.BlockHash, Time: info.BlockTime, Index: info.Index}
		v.Confirmations = confirmations(headHeight, info.Height)
	}
	return v
}

func direction(tx *core.Tx, addr core.Address) string {
	switch {
	case tx.From == addr && tx.To == addr:
		return "self"
	case tx.From == addr:
		return "out"
	default:
		return "in"
	}
}

func confirmations(head, height uint64) uint64 {
	if height > head {
		return 0
	}
	return head - height + 1
}

func summary(b *core.Block, headHeight uint64) BlockSummary {
	return BlockSummary{
		Height:        b.Height,
		Hash:          b.Hash,
		PrevHash:      b.PrevHash,
		Time:          b.Time,
		TxRoot:        b.TxRoot,
		Producer:      b.Producer,
		TxCount:       len(b.Txs),
		Confirmations: confirmations(headHeight, b.Height),
	}
}

func (s *server) blockView(b *core.Block, headHeight uint64) BlockView {
	txs := make([]TxView, len(b.Txs))
	for i := range b.Txs {
		txs[i] = s.txView(confirmedInfo(b, i), headHeight)
	}
	return BlockView{BlockSummary: summary(b, headHeight), Txs: txs}
}

func confirmedInfo(b *core.Block, i int) chain.TxInfo {
	return chain.TxInfo{
		Tx: b.Txs[i], Hash: b.Txs[i].Hash(), Status: chain.TxConfirmed,
		Height: b.Height, Index: uint32(i), BlockHash: b.Hash, BlockTime: b.Time,
	}
}
