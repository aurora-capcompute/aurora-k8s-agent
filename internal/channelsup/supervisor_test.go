package channelsup

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"aurora-k8s-agent/internal/apis/aurora/v1alpha1"
	"aurora-k8s-agent/internal/binding"
	"aurora-k8s-agent/internal/controller"
	"aurora-k8s-agent/internal/secretbox"
	"aurora-k8s-agent/internal/secrets"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeBridges records starts/stops/reroutes keyed by channel name, with an
// injectable starter.
type fakeBridges struct {
	mu       sync.Mutex
	starts   map[string]int
	stops    map[string]int
	reroutes map[string]int
}

func newFakeBridges() *fakeBridges {
	return &fakeBridges{starts: map[string]int{}, stops: map[string]int{}, reroutes: map[string]int{}}
}

func (f *fakeBridges) starter(name string) starter {
	return func(ch controller.ResolvedChannel, _ map[string][]byte, hash string) (*managed, error) {
		f.mu.Lock()
		f.starts[ch.Name]++
		f.mu.Unlock()
		done := make(chan struct{})
		var once sync.Once
		return &managed{
			tokenHash:  hash,
			cancel:     func() { once.Do(func() { close(done) }) }, // bridge exits on cancel
			done:       done,
			closeStore: func() error { f.mu.Lock(); f.stops[ch.Name]++; f.mu.Unlock(); return nil },
			setPolicies: func([]binding.Resolved) {
				f.mu.Lock()
				f.reroutes[ch.Name]++
				f.mu.Unlock()
			},
		}, nil
	}
}

func (f *fakeBridges) count(m map[string]int, name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m[name]
}

func sealed(t *testing.T, key, token string) v1alpha1.SecretSource {
	t.Helper()
	ct, err := secretbox.SealBase64(secretbox.DeriveKey(key), []byte(token))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return v1alpha1.SecretSource{Type: v1alpha1.SecretInPlaceEncrypted, Ciphertext: ct}
}

func telegramChannel(t *testing.T, name, key, token string, bindings ...binding.Resolved) controller.ResolvedChannel {
	return controller.ResolvedChannel{
		Kind: v1alpha1.KindTelegramChannel, Name: name, Source: "telegram",
		Secrets:  map[string]v1alpha1.SecretSource{"botToken": sealed(t, key, token)},
		Bindings: bindings,
	}
}

func newTestSupervisor(t *testing.T, key string, fake *fakeBridges) *Supervisor {
	s := New(nil, secrets.NewInPlace(key), t.TempDir(), nil, "", quietLogger())
	s.starters = map[string]starter{"telegram": fake.starter("telegram")}
	s.ctx = context.Background() // pretend Start ran
	return s
}

func TestSupervisorAddUpdateRemove(t *testing.T) {
	const key = "secret-key"
	fake := newFakeBridges()
	s := newTestSupervisor(t, key, fake)

	// 1. Add a channel.
	s.reconcile(controller.Resolved{Channels: []controller.ResolvedChannel{
		telegramChannel(t, "ops", key, "tok-1"),
	}})
	if fake.count(fake.starts, "ops") != 1 {
		t.Fatalf("expected ops started once, got %d", fake.count(fake.starts, "ops"))
	}

	// 2. Same token, changed bindings → hot-swap, no restart.
	s.reconcile(controller.Resolved{Channels: []controller.ResolvedChannel{
		telegramChannel(t, "ops", key, "tok-1", binding.Resolved{Users: []string{"1"}, Scopes: []string{"2"}}),
	}})
	if fake.count(fake.starts, "ops") != 1 {
		t.Fatalf("binding change must not restart: starts=%d", fake.count(fake.starts, "ops"))
	}
	if fake.count(fake.reroutes, "ops") != 1 {
		t.Fatalf("binding change should hot-swap once: reroutes=%d", fake.count(fake.reroutes, "ops"))
	}

	// 3. Token change → restart (stop + start).
	s.reconcile(controller.Resolved{Channels: []controller.ResolvedChannel{
		telegramChannel(t, "ops", key, "tok-2"),
	}})
	if fake.count(fake.starts, "ops") != 2 || fake.count(fake.stops, "ops") != 1 {
		t.Fatalf("token change should restart: starts=%d stops=%d", fake.count(fake.starts, "ops"), fake.count(fake.stops, "ops"))
	}

	// 4. Channel removed → stop (the store is closed asynchronously).
	s.reconcile(controller.Resolved{})
	eventually(t, func() bool { return fake.count(fake.stops, "ops") == 2 },
		"removed channel should stop")
}

// eventually polls cond for up to ~1s.
func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func TestResolveTokensSecretLoop(t *testing.T) {
	const key = "k"
	s := New(nil, secrets.NewInPlace(key), t.TempDir(), nil, "", quietLogger())

	ch := telegramChannel(t, "ops", key, "xoxb-token")
	tokens, hash1, err := s.resolveTokens(ch)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(tokens["botToken"]) != "xoxb-token" {
		t.Fatalf("token = %q, want xoxb-token", tokens["botToken"])
	}

	// Same token → same hash; different token → different hash.
	_, hash2, _ := s.resolveTokens(telegramChannel(t, "ops", key, "xoxb-token"))
	if hash1 != hash2 {
		t.Fatal("same token should hash equal")
	}
	_, hash3, _ := s.resolveTokens(telegramChannel(t, "ops", key, "other"))
	if hash1 == hash3 {
		t.Fatal("different token should hash differently")
	}
}
