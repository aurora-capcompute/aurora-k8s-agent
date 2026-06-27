package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/brainspec"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/oci"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secretbox"
)

// sealSecret reads a plaintext credential from stdin and prints the base64
// nonce‖AES-GCM ciphertext for an inPlaceEncrypted channel secret, using the same
// key (AURORA_SECRET_KEY) the agent decrypts with. Usage:
//
//	printf %s "$TOKEN" | aurora-k8s-agent seal-secret
func sealSecret() error {
	keyValue, err := requiredSecret("AURORA_SECRET_KEY", "AURORA_SECRET_KEY_FILE")
	if err != nil {
		return err
	}
	plain, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	plain = bytes.TrimRight(plain, "\r\n")
	out, err := secretbox.SealBase64(secretbox.DeriveKey(keyValue), plain)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

// packBrain compiles a brain manifest + wasm into an on-disk OCI image layout so
// it can be loaded with no registry — referenced as "oci-layout:<dir>:<tag>" from
// a Brain CRD's artifact or from AURORA_BRAINS, locally or baked into an image.
// Usage:
//
//	aurora-k8s-agent pack-brain --wasm brain.wasm --manifest manifest.json --out ./layout [--tag latest]
func packBrain(args []string) error {
	fs := flag.NewFlagSet("pack-brain", flag.ContinueOnError)
	wasmPath := fs.String("wasm", "", "path to the compiled brain wasm")
	manifestPath := fs.String("manifest", "", "path to the brain manifest (brainspec JSON)")
	outDir := fs.String("out", "", "output directory for the OCI layout")
	tag := fs.String("tag", "latest", "tag for the packed artifact")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *wasmPath == "" || *manifestPath == "" || *outDir == "" {
		return errors.New("--wasm, --manifest and --out are required")
	}
	wasm, err := os.ReadFile(*wasmPath)
	if err != nil {
		return fmt.Errorf("read wasm: %w", err)
	}
	manifestJSON, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	// Validate the manifest (and its ABI) up front so packing fails fast on a bad
	// brain rather than at load time.
	spec, err := brainspec.Parse(manifestJSON)
	if err != nil {
		return err
	}
	if err := spec.CheckABI(); err != nil {
		return err
	}
	digest, err := oci.WriteLayout(context.Background(), *outDir, *tag, manifestJSON, wasm)
	if err != nil {
		return err
	}
	fmt.Printf("packed brain %q -> oci-layout:%s:%s (%s)\n", spec.ID, *outDir, *tag, digest)
	return nil
}
