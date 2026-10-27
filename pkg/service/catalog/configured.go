package catalog

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ReadConfiguredFile reads explicitly curated replay candidates. This optional
// source is never written, learned from, or used as Favorites storage.
func ReadConfiguredFile(path string) ([]Entry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open configured catalog: %w", err)
	}
	defer file.Close()

	const maxBytes = 8 << 20

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read configured catalog: %w", err)
	}

	if len(data) > maxBytes {
		return nil, fmt.Errorf("configured catalog exceeds %d bytes", maxBytes)
	}

	var doc struct {
		Version int     `json:"version"`
		Entries []Entry `json:"entries"`
	}
	if err = json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode configured catalog: %w", err)
	}

	if doc.Version != 1 || doc.Entries == nil {
		return nil, fmt.Errorf("unsupported configured catalog schema")
	}

	for i := range doc.Entries {
		if strings.TrimSpace(doc.Entries[i].Source) == "" || strings.TrimSpace(doc.Entries[i].Location) == "" {
			return nil, fmt.Errorf("configured catalog entry %d needs source and location", i)
		}
	}

	return doc.Entries, nil
}
