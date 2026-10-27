package soundtouchweb

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gesellix/bose-soundtouch/pkg/service/catalog"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

// CatalogPayload is what the player receives from GET /api/control/catalog.
//
// Available says whether there is a catalog at all, rather than leaving the
// player to guess from an empty list: a standalone soundtouch-player has no
// learned datastore behind it, but may supply a configured file. An operator
// can also switch the learned catalog off. These cases
// mean "no pick list here", which is a different thing to tell the owner than
// "nothing seen yet".
type CatalogPayload struct {
	Available bool            `json:"available"`
	Entries   []catalog.Entry `json:"entries"`
}

// HandleCatalog returns curated candidates followed by the learned catalog.
func (app *WebApp) HandleCatalog(w http.ResponseWriter, _ *http.Request) {
	payload := CatalogPayload{Entries: []catalog.Entry{}}

	if app.ConfiguredCatalog != nil {
		payload.Available = true

		entries, err := app.ConfiguredCatalog()
		if err != nil {
			log.Printf("Configured catalog read failed: %v", err)
			app.sendError(w, "The configured catalog could not be loaded.", http.StatusInternalServerError)

			return
		}

		payload.Entries = append(payload.Entries, entries...)
	}

	if app.CatalogEntries != nil {
		payload.Available = true

		if entries := app.CatalogEntries(); entries != nil {
			payload.Entries = append(payload.Entries, entries...)
		}
	}

	// Curated entries keep their explicit order and metadata; learned aliases
	// remain useful but do not duplicate them in the picker.
	seen := make(map[string]bool, len(payload.Entries))

	merged := make([]catalog.Entry, 0, len(payload.Entries))
	for i := range payload.Entries {
		entry := &payload.Entries[i]

		key := entry.Identity()
		if !seen[key] {
			seen[key] = true

			merged = append(merged, *entry)
		}
	}

	payload.Entries = merged

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(webtypes.APIResponse{Success: true, Data: payload}); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// SetCatalogFile wires an optional operator-configured path, not a path from
// the control API. Each request reads the file; updates need no player restart.
func (app *WebApp) SetCatalogFile(path string) {
	if path == "" {
		app.ConfiguredCatalog = nil
		return
	}

	app.ConfiguredCatalog = func() ([]catalog.Entry, error) {
		return catalog.ReadConfiguredFile(path)
	}
}
