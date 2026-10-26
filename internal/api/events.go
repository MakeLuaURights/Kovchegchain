package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/core"
)

const (
	maxReplay    = 1000             // сколько старых блоков можно догнать при переподключении
	pingInterval = 15 * time.Second // пустой комментарий, чтобы соединение не закрыли как неактивное
)

// events — поток событий Server-Sent Events.
//
//	?types=block,tx  какие события слать (по умолчанию оба; с ?address= — только tx)
//	?address=kvg...  только транзакции этого адреса
//	?since=N         сначала прислать блоки после N (то же делает заголовок Last-Event-ID)
//
// id каждого события — номер блока, поэтому браузерный EventSource после обрыва
// сам продолжит с нужного места.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var addr core.Address
	if a := q.Get("address"); a != "" {
		var err error
		if addr, err = core.ParseAddress(a); err != nil {
			writeErr(w, err)
			return
		}
	}
	wantBlock, wantTx := true, true
	if addr != "" {
		wantBlock = false
	}
	if t := q.Get("types"); t != "" {
		wantBlock, wantTx = false, false
		for _, name := range strings.Split(t, ",") {
			switch strings.TrimSpace(name) {
			case "block":
				wantBlock = true
			case "tx":
				wantTx = true
			default:
				writeError(w, http.StatusBadRequest, "bad_request", fmt.Errorf("неизвестный тип события %q", name))
				return
			}
		}
	}
	since, hasSince := r.Header.Get("Last-Event-ID"), true
	if since == "" {
		since = q.Get("since")
	}
	var last uint64
	if since == "" {
		hasSince = false
	} else {
		n, err := strconv.ParseUint(since, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", errors.New("неверный since / Last-Event-ID"))
			return
		}
		last = n
	}

	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		writeError(w, http.StatusServiceUnavailable, "too_many_streams", errors.New("слишком много подключений к потоку событий"))
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // поток живёт сколько угодно

	// Подписка до чтения головы: блоки, появившиеся между ними, не потеряются.
	blocks, cancel := s.c.Subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}

	send := func(b *core.Block) error {
		head := s.c.Head().Height
		if wantBlock {
			if err := writeEvent(w, b.Height, "block", summary(b, head)); err != nil {
				return err
			}
		}
		if wantTx {
			for i := range b.Txs {
				tx := &b.Txs[i]
				if addr != "" && tx.From != addr && tx.To != addr {
					continue
				}
				v := s.txView(confirmedInfo(b, i), head)
				if addr != "" {
					v.Direction = direction(tx, addr)
				}
				if err := writeEvent(w, b.Height, "tx", v); err != nil {
					return err
				}
			}
		}
		return rc.Flush()
	}

	head := s.c.Head().Height
	if hasSince && last < head {
		from := last + 1
		if head-last > maxReplay {
			from = head - maxReplay + 1
		}
		for n := from; n <= head; n++ {
			b, err := s.c.BlockByHeight(n)
			if err != nil {
				return
			}
			if err := send(b); err != nil {
				return
			}
		}
	}
	// Дальше — только новые блоки. Если since больше высоты (цепочку пересоздали),
	// тоже продолжаем с текущей головы, а не ждём несуществующий номер.
	last = head
	if err := rc.Flush(); err != nil {
		return
	}

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b, ok := <-blocks:
			if !ok {
				return // не успевали читать — клиент переподключится с Last-Event-ID
			}
			if b.Height <= last {
				continue
			}
			if err := send(b); err != nil {
				return
			}
			last = b.Height
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, id uint64, event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, event, data)
	return err
}
