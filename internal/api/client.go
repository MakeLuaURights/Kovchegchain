package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/core"
)

// Client — HTTP-клиент к API v1 узла.
type Client struct {
	Base string // например "http://127.0.0.1:7070"
	HTTP *http.Client
}

// Error — ошибка, которую вернул узел.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return "узел: " + e.Message }

// NewClient создаёт клиента.
func NewClient(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Status — GET /v1/status.
func (c *Client) Status() (*StatusView, error) {
	var r StatusView
	return &r, c.do(http.MethodGet, "/v1/status", nil, &r)
}

// Account — GET /v1/accounts/{address}.
func (c *Client) Account(addr core.Address) (*AccountView, error) {
	var r AccountView
	return &r, c.do(http.MethodGet, "/v1/accounts/"+url.PathEscape(string(addr)), nil, &r)
}

// AccountTxs — GET /v1/accounts/{address}/txs.
func (c *Client) AccountTxs(addr core.Address, before string, limit int) (*TxList, error) {
	q := url.Values{}
	if before != "" {
		q.Set("before", before)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var r TxList
	return &r, c.do(http.MethodGet, "/v1/accounts/"+url.PathEscape(string(addr))+"/txs?"+q.Encode(), nil, &r)
}

// SubmitTx — POST /v1/txs. С wait=true ждёт подтверждения не дольше timeout.
func (c *Client) SubmitTx(tx *core.Tx, wait bool, timeout time.Duration) (*SubmitView, error) {
	path := "/v1/txs"
	if wait {
		path += "?wait=true&timeout=" + strconv.Itoa(max(1, int(timeout/time.Second)))
	}
	var r SubmitView
	return &r, c.do(http.MethodPost, path, tx, &r)
}

// Tx — GET /v1/txs/{hash}.
func (c *Client) Tx(h core.Hash) (*TxView, error) {
	var r TxView
	return &r, c.do(http.MethodGet, "/v1/txs/"+h.String(), nil, &r)
}

// Block — GET /v1/blocks/{id}: номер, хэш или "latest".
func (c *Client) Block(id string) (*BlockView, error) {
	var r BlockView
	return &r, c.do(http.MethodGet, "/v1/blocks/"+url.PathEscape(id), nil, &r)
}

// Blocks — GET /v1/blocks.
func (c *Client) Blocks(before string, limit int) (*BlockList, error) {
	q := url.Values{}
	if before != "" {
		q.Set("before", before)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var r BlockList
	return &r, c.do(http.MethodGet, "/v1/blocks?"+q.Encode(), nil, &r)
}

func (c *Client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.Base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("узел недоступен (%s): %w", c.Base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e ErrorView
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return &Error{Status: resp.StatusCode, Code: e.Code, Message: e.Error}
		}
		return &Error{Status: resp.StatusCode, Code: "http", Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
