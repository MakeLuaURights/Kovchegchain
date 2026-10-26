package core

import (
	"encoding/hex"
	"encoding/json"
)

// HexBytes — байты, которые в JSON записываются hex-строкой.
type HexBytes []byte

func (b HexBytes) String() string { return hex.EncodeToString(b) }

func (b HexBytes) MarshalJSON() ([]byte, error) { return json.Marshal(b.String()) }

func (b *HexBytes) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	d, err := hex.DecodeString(s)
	if err != nil {
		return err
	}
	*b = d
	return nil
}
