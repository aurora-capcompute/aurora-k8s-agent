// Package brainspec describes the OCI config blob carried inside a brain artifact.
// The config is intentionally minimal: it declares which WASM binaries are bundled
// and which one is the entry-point. Capability declarations, children, and system
// prompts all live in the ChannelBinding CRD — not in the OCI artifact — so
// changing the delegation tree never requires a repack.
package brainspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ABIVersion is the host-call ABI this engine implements.
const ABIVersion = 1

// ErrIncompatibleABI reports a brain built against an ABI this engine does not implement.
var ErrIncompatibleABI = errors.New("incompatible brain ABI")

// Manifest is the brain's OCI config blob. It names which WASM binaries are
// bundled in the artifact and which one is the entry-point. Capability
// declarations and children are operator-supplied via the ChannelBinding CRD.
type Manifest struct {
	ABI    int      `json:"abi"`
	Main   string   `json:"main"`
	Brains []string `json:"brains,omitempty"`
}

// CheckABI gates a brain manifest against the ABI this host implements.
func (m Manifest) CheckABI() error {
	if m.ABI != ABIVersion {
		return fmt.Errorf("%w: main brain %q declares ABI %d, host implements %d",
			ErrIncompatibleABI, m.Main, m.ABI, ABIVersion)
	}
	return nil
}

// Parse decodes and validates a brain manifest.
func Parse(raw []byte) (Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("decode brain manifest: %w", err)
	}
	m.Main = strings.TrimSpace(m.Main)
	if m.Main == "" {
		return Manifest{}, errors.New("brain manifest 'main' is required")
	}
	for i := range m.Brains {
		m.Brains[i] = strings.TrimSpace(m.Brains[i])
		if m.Brains[i] == "" {
			return Manifest{}, fmt.Errorf("brain name at index %d is empty", i)
		}
	}
	// Default brains list to [main] when omitted.
	if len(m.Brains) == 0 {
		m.Brains = []string{m.Main}
	}
	mainFound := false
	for _, b := range m.Brains {
		if b == m.Main {
			mainFound = true
			break
		}
	}
	if !mainFound {
		return Manifest{}, fmt.Errorf("brain manifest 'main' %q is not in 'brains' list", m.Main)
	}
	return m, nil
}
