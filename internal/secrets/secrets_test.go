package secrets

import (
	"errors"
	"testing"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secretbox"
)

func TestInPlaceRoundTrip(t *testing.T) {
	key := secretbox.DeriveKey("test-secret-key")
	ciphertext, err := secretbox.SealBase64(key, []byte("xoxb-the-token"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	r := NewInPlace("test-secret-key")
	got, err := r.Resolve(v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: ciphertext})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(got) != "xoxb-the-token" {
		t.Fatalf("got %q, want the token", got)
	}
}

func TestInPlaceWrongKeyFails(t *testing.T) {
	ciphertext, err := secretbox.SealBase64(secretbox.DeriveKey("right"), []byte("secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	r := NewInPlace("wrong")
	if _, err := r.Resolve(v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: ciphertext}); err == nil {
		t.Fatal("decrypt with the wrong key should fail the auth tag")
	}
}

func TestSecretStorageUnsupported(t *testing.T) {
	r := NewInPlace("k")
	_, err := r.Resolve(v1alpha1.SecretSource{Type: v1alpha1.SecretStorage, Ref: &v1alpha1.SecretKeyRef{Name: "s", Key: "k"}})
	if !errors.Is(err, ErrUnsupportedVariant) {
		t.Fatalf("secretStorage should be unsupported, got %v", err)
	}
}
