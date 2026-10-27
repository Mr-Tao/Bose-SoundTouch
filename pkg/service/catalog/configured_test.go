package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredCatalogPreservesReplayAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	for _, body := range []string{`null`, `{"version":2,"entries":[]}`, `{"version":1}`, `{"version":1,"entries":[{"source":"TUNEIN"}]}`, `{"version":1,"entries":[]} {}`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadConfiguredFile(path); err == nil {
			t.Errorf("accepted invalid catalog %s", body)
		}
		original, err := os.ReadFile(path)
		if err != nil || string(original) != body {
			t.Fatal("read modified source")
		}
	}
	body := `{"version":1,"entries":[{"source":"TUNEIN","source_account":"account","location":"opaque:station","type":"station","name":"Chosen name"}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadConfiguredFile(path)
	if err != nil || len(entries) != 1 || entries[0].SourceAccount != "account" || entries[0].Location != "opaque:station" || entries[0].Type != "station" {
		t.Fatalf("replay changed: %+v %v", entries, err)
	}
}
