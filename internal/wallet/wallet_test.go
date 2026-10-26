package wallet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "wallet.json")
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("права %v, нужны 0600", fi.Mode().Perm())
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Address != w.Address || !back.Private.Equal(w.Private) {
		t.Fatal("загруженный кошелёк отличается")
	}
	// Повторное создание не затирает старый ключ.
	w2, _ := New()
	if err := w2.Save(path); err == nil || !strings.Contains(err.Error(), "уже существует") {
		t.Fatalf("перезапись: %v", err)
	}
	if again, _ := Load(path); again.Address != w.Address {
		t.Fatal("старый кошелёк испорчен")
	}
}

func TestLoadRejectsMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wallet.json")
	w, _ := New()
	w.Save(path)
	data, _ := os.ReadFile(path)
	other, _ := New()
	bad := strings.Replace(string(data), string(w.Address), string(other.Address), 1)
	os.WriteFile(path, []byte(bad), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("кошелёк с чужим адресом загружен")
	}
}
