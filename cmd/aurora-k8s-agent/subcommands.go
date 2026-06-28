package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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

// brainFlag accumulates --brain name:path flags.
type brainFlag []string

func (b *brainFlag) String() string  { return fmt.Sprint([]string(*b)) }
func (b *brainFlag) Set(v string) error {
	if !strings.Contains(v, ":") {
		return errors.New("--brain must be name:path (e.g. kubernetes-agent:brain.wasm)")
	}
	*b = append(*b, v)
	return nil
}

// packBrain packs one or more WASM binaries into an OCI image layout so it can
// be loaded with no registry — referenced as "oci-layout:<dir>:<tag>" from a
// Brain CRD's artifact field. Usage:
//
//	aurora-k8s-agent pack-brain \
//	  --brain kubernetes-agent:brain.wasm \
//	  --brain k8s-scout:scout.wasm \
//	  --main kubernetes-agent \
//	  --out ./layout [--tag latest]
//
// If only one --brain is given and --main is omitted, main defaults to that
// brain's name.
func packBrain(args []string) error {
	fs := flag.NewFlagSet("pack-brain", flag.ContinueOnError)
	var brainFlags brainFlag
	fs.Var(&brainFlags, "brain", "name:path pair (repeatable); e.g. --brain kubernetes-agent:brain.wasm")
	mainName := fs.String("main", "", "entry-point brain name (defaults to the sole --brain name)")
	outDir := fs.String("out", "", "output directory for the OCI layout")
	tag := fs.String("tag", "latest", "tag for the packed artifact")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(brainFlags) == 0 || *outDir == "" {
		return errors.New("--brain and --out are required")
	}

	brains := make(map[string][]byte, len(brainFlags))
	brainNames := make([]string, 0, len(brainFlags))
	for _, bf := range brainFlags {
		i := strings.Index(bf, ":")
		name, path := bf[:i], bf[i+1:]
		wasm, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read wasm %q: %w", path, err)
		}
		brains[name] = wasm
		brainNames = append(brainNames, name)
	}

	main := *mainName
	if main == "" {
		if len(brainNames) != 1 {
			return errors.New("--main is required when multiple --brain flags are given")
		}
		main = brainNames[0]
	}
	if _, ok := brains[main]; !ok {
		return fmt.Errorf("--main %q is not among the --brain names", main)
	}

	configJSON, err := json.Marshal(brainspec.Manifest{ABI: brainspec.ABIVersion, Main: main, Brains: brainNames})
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// Validate before writing so packing fails fast on a bad manifest.
	if _, err := brainspec.Parse(configJSON); err != nil {
		return err
	}

	digest, err := oci.WriteLayout(context.Background(), *outDir, *tag, configJSON, brains)
	if err != nil {
		return err
	}
	fmt.Printf("packed brain %q -> oci-layout:%s:%s (%s)\n", main, *outDir, *tag, digest)
	return nil
}
