package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// NanoPerKVG — сколько нано-KVG в одной монете KVG (10^9).
const NanoPerKVG uint64 = 1_000_000_000

// Decimals — количество знаков после запятой у KVG.
const Decimals = 9

// ErrBadAmount — сумма записана неверно.
var ErrBadAmount = errors.New("неверная сумма")

// ParseAmount переводит строку в KVG ("10", "0.5", "1.000000001") в нано-KVG.
// Плавающая точка не используется: строка разбирается как десятичная запись.
func ParseAmount(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrBadAmount
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" && (!hasDot || frac == "") {
		return 0, ErrBadAmount
	}
	if hasDot && frac == "" {
		return 0, ErrBadAmount
	}
	if len(frac) > Decimals {
		return 0, fmt.Errorf("%w: больше %d знаков после точки", ErrBadAmount, Decimals)
	}
	if !isDigits(whole) || !isDigits(frac) {
		return 0, ErrBadAmount
	}

	var w uint64
	if whole != "" {
		var err error
		w, err = strconv.ParseUint(whole, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: слишком большая", ErrBadAmount)
		}
	}
	var f uint64
	if frac != "" {
		frac += strings.Repeat("0", Decimals-len(frac))
		f, _ = strconv.ParseUint(frac, 10, 64)
	}
	if w > (^uint64(0)-f)/NanoPerKVG {
		return 0, fmt.Errorf("%w: слишком большая", ErrBadAmount)
	}
	return w*NanoPerKVG + f, nil
}

// FormatAmount переводит нано-KVG в строку KVG без лишних нулей: 1500000000 -> "1.5".
func FormatAmount(nano uint64) string {
	whole := nano / NanoPerKVG
	frac := nano % NanoPerKVG
	if frac == 0 {
		return strconv.FormatUint(whole, 10)
	}
	fs := fmt.Sprintf("%09d", frac)
	fs = strings.TrimRight(fs, "0")
	return strconv.FormatUint(whole, 10) + "." + fs
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
