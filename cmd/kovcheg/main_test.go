package main

import (
	"flag"
	"strings"
	"testing"
)

func TestParseFlagsAnywhere(t *testing.T) {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fee := fs.String("fee", "", "")
	wait := fs.Bool("wait", false, "")
	pos, err := parseFlags(fs, []string{"kvgaaa", "-fee", "0.01", "12.5", "-wait"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pos, " ") != "kvgaaa 12.5" || *fee != "0.01" || !*wait {
		t.Fatalf("pos=%v fee=%q wait=%v", pos, *fee, *wait)
	}
}
