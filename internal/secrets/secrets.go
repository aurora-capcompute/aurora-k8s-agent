// Package secrets resolves a channel credential (a v1alpha1.SecretSource tagged
// union) into its plaintext value. Only the "inPlaceEncrypted" variant is
// implemented: the ciphertext lives in the CRD and is decrypted in place with the
// agent's secret key. The "secretStorage" variant (external k8s Secret / fs) is
// declared but not yet resolvable.
package secrets

import (
	"errors"
	"fmt"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secretbox"
)

// ErrUnsupportedVariant reports a SecretSource variant this resolver cannot
// resolve yet (currently: secretStorage).
var ErrUnsupportedVariant = errors.New("unsupported secret variant")

// Resolver turns a SecretSource into plaintext bytes.
type Resolver interface {
	Resolve(src v1alpha1.SecretSource) ([]byte, error)
}

// InPlace resolves the inPlaceEncrypted variant by decrypting the embedded
// ciphertext with a single agent-held key.
type InPlace struct {
	key []byte
}

// NewInPlace builds an in-place resolver from a configured key value (derived the
// same way as the agent's state key).
func NewInPlace(keyValue string) *InPlace {
	return &InPlace{key: secretbox.DeriveKey(keyValue)}
}

// Resolve implements Resolver.
func (r *InPlace) Resolve(src v1alpha1.SecretSource) ([]byte, error) {
	switch src.Type {
	case v1alpha1.SecretInPlaceEncrypted:
		if src.Ciphertext == "" {
			return nil, errors.New("inPlaceEncrypted: ciphertext is empty")
		}
		plain, err := secretbox.OpenBase64(r.key, src.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt in-place secret: %w", err)
		}
		return plain, nil
	case v1alpha1.SecretStorage:
		return nil, fmt.Errorf("%w: secretStorage is not implemented", ErrUnsupportedVariant)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedVariant, src.Type)
	}
}
