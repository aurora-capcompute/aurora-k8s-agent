// Package brainspec describes the manifest that ships inside a brain's OCI
// artifact: the capabilities the brain uses, each flagged optional or required.
// This declared set is the superset a function instance is checked against — the
// instance may grant a subset, but every non-optional capability must be present.
// Declaration only: capability implementations remain host-side dispatchers.
package brainspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ABIVersion is the host-call ABI this engine implements: the lifecycle contract
// by which a brain receives its run input (the agent.input host call) and reports
// its answer (agent.finish). A brain artifact declares the ABI it was built
// against so the host can refuse to run a brain it is not compatible with.
const ABIVersion = 1

// ErrIncompatibleABI reports a brain built against a host-call ABI this engine
// does not implement.
var ErrIncompatibleABI = errors.New("incompatible brain ABI")

// Manifest is the brain's self-description, carried as the OCI config blob.
type Manifest struct {
	ID           string       `json:"id"`
	ABI          int          `json:"abi"`
	Capabilities []Capability `json:"capabilities"`
}

// CheckABI gates a brain manifest against the ABI this host implements. A brain
// must declare exactly ABIVersion; any other value (including an undeclared ABI
// of 0) is refused so an incompatible brain never runs against this lifecycle.
func (m Manifest) CheckABI() error {
	if m.ABI != ABIVersion {
		return fmt.Errorf("%w: brain %q declares ABI %d, host implements %d",
			ErrIncompatibleABI, m.ID, m.ABI, ABIVersion)
	}
	return nil
}

// Capability is one capability the brain declares. Optional capabilities may be
// omitted by a function instance; required (Optional=false) ones may not.
type Capability struct {
	Name     string          `json:"name"`
	Optional bool            `json:"optional,omitempty"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

// Parse decodes and validates a brain manifest.
func Parse(raw []byte) (Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("decode brain manifest: %w", err)
	}
	m.ID = strings.TrimSpace(m.ID)
	if m.ID == "" {
		return Manifest{}, errors.New("brain manifest id is required")
	}
	seen := make(map[string]struct{}, len(m.Capabilities))
	for i := range m.Capabilities {
		m.Capabilities[i].Name = strings.TrimSpace(m.Capabilities[i].Name)
		name := m.Capabilities[i].Name
		if name == "" {
			return Manifest{}, fmt.Errorf("capability %d name is required", i)
		}
		if _, dup := seen[name]; dup {
			return Manifest{}, fmt.Errorf("duplicate capability %q", name)
		}
		seen[name] = struct{}{}
	}
	return m, nil
}

// Declared returns the declared capability and whether the brain declares it.
func (m Manifest) Declared(name string) (Capability, bool) {
	for _, c := range m.Capabilities {
		if c.Name == name {
			return c, true
		}
	}
	return Capability{}, false
}

// Required returns the names of the brain's non-optional capabilities.
func (m Manifest) Required() []string {
	var out []string
	for _, c := range m.Capabilities {
		if !c.Optional {
			out = append(out, c.Name)
		}
	}
	return out
}
