package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Current mirrors node_modules/.open-slide/current.json, the live cursor the
// dev server writes as the user navigates in the browser.
type Current map[string]any

// ReadCurrent returns the cursor, or nil when the dev server has not written one.
func (s *Store) ReadCurrent() (Current, error) {
	b, err := os.ReadFile(filepath.Join(s.root, "node_modules", ".open-slide", "current.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Current
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return c, nil
}
