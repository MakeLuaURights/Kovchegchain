package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/core"
)

// Client — HTTP-клиент к узлу.
type Client struct {
	Base string // например "http://127.0.0.1:7070"
	HTTP *http.Client
}

// NewClient создаёт клиента с таймаутом.
func NewClient(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Info — GET /info.
func (c *Client) Info() (*InfoResponse, error) {
	var r InfoResponse
	return &r, c.do(http.MethodGet, "/info", nil, &r)
}

// Balance — GET /balance.
func (c *Client) Balance(addr core.Address) (*BalanceResponse, error) {
	var r BalanceResponse
	return &r, c.do(http.MethodGet, "/balance?address="+url.QueryEscape(string(addr)), nil, &r)
}

// SubmitTx — POST /tx.
func (c *Client) SubmitTx(tx *core.Tx) (core.Hash, error) {
	var r SubmitResponse
	err := c.do(http.MethodPost, "/tx", tx, &r)
	return r.Hash, err
}

// Tx — GET /tx.
func (c *Client) Tx(h core.Hash) (*TxResponse, error) {
	var r TxResponse
	return &r, c.do(http.MethodGet, "/tx?hash="+h.String(), nil, &r)
}

func (c *Client) do(method, path string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
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
		var e errorResponse
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return fmt.Errorf("узел: %s", e.Error)
		}
		return fmt.Errorf("узел: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
