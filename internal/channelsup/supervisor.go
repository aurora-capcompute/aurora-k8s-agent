// Package channelsup supervises live chat bridges driven by the control plane:
// one Slack/Telegram client per channel declared in a Manifest, with tokens
// resolved from the channel's SecretSource and routing derived from the bindings
// that target it. It is a single source.Source; as the control plane re-reconciles
// it adds, hot-swaps (routing only), or stops bridges so the Manifests' channels
// behave like live instances.
package channelsup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aurora-capcompute/aurora-capcompute/aurora"

	"github.com/aurora-capcompute/aurora-k8s-agent/internal/binding"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/controller"
	"github.com/aurora-capcompute/aurora-k8s-agent/internal/secrets"

	tgchat "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram"
	tgpolicy "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram/policy"
	tgstate "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/telegram/state"
	tgapi "github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/telegram"

	slchat "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/slack"
	slpolicy "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/slack/policy"
	slstate "github.com/aurora-capcompute/aurora-k8s-agent/internal/chat/slack/state"
	slapi "github.com/aurora-capcompute/aurora-k8s-agent/internal/transport/slack"
)

// Warmer resolves capability settings for a set of bindings and caches them so
// that NewDispatcher can build configured dispatchers without re-decrypting
// secrets on every call.
type Warmer interface {
	Warmup(bindings []binding.Resolved) error
}

// Supervisor manages per-channel bridges from control-plane snapshots.
type Supervisor struct {
	runtime         aurora.Runtime
	resolver        secrets.Resolver
	warmer          Warmer
	dataDir         string
	stateKey        []byte
	telegramBaseURL string
	logger          *slog.Logger

	apply sync.Mutex // serialises reconciliation of the running set

	// starters builds a bridge per transport; overridable in tests.
	starters map[string]starter

	mu      sync.Mutex
	ctx     context.Context
	pending *controller.Resolved
	running map[string]*managed
}

// starter constructs and launches one bridge, returning its handle.
type starter func(ch controller.ResolvedChannel, tokens map[string][]byte, hash string) (*managed, error)

// managed is one running bridge.
type managed struct {
	tokenHash   string
	cancel      context.CancelFunc
	done        chan struct{}
	setPolicies func([]binding.Resolved)
	closeStore  func() error
}

// New builds a Supervisor. warmer is called at channel start and on hot-swap to
// resolve capability secrets; baseURL overrides the Telegram API endpoint (for
// tests and the kind smoke); empty uses the default.
func New(runtime aurora.Runtime, resolver secrets.Resolver, warmer Warmer, dataDir string, stateKey []byte, telegramBaseURL string, logger *slog.Logger) *Supervisor {
	s := &Supervisor{
		runtime: runtime, resolver: resolver, warmer: warmer,
		dataDir: dataDir, stateKey: stateKey,
		telegramBaseURL: telegramBaseURL, logger: logger,
		running: make(map[string]*managed),
	}
	s.starters = map[string]starter{
		"telegram": s.startTelegram,
		"slack":    s.startSlack,
	}
	return s
}

// Kind implements source.Source.
func (s *Supervisor) Kind() string { return "channel-supervisor" }

// Start serves until ctx is cancelled, applying any snapshot that arrived before
// Start and then stopping every bridge on shutdown.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()

	if pending != nil {
		s.reconcile(*pending)
	}
	<-ctx.Done()
	s.stopAll()
	return ctx.Err()
}

// Apply receives a control-plane snapshot. Before Start it is buffered (latest
// wins); after, it reconciles the running set immediately.
func (s *Supervisor) Apply(res controller.Resolved) {
	s.mu.Lock()
	if s.ctx == nil {
		r := res
		s.pending = &r
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.reconcile(res)
}

// reconcile diffs the desired channels against the running set: add new bridges,
// hot-swap routing when only bindings changed, restart on a token change, and
// stop bridges whose channel disappeared.
func (s *Supervisor) reconcile(res controller.Resolved) {
	s.apply.Lock()
	defer s.apply.Unlock()

	desired := make(map[string]controller.ResolvedChannel)
	for _, ch := range res.Channels {
		if ch.Source == "slack" || ch.Source == "telegram" {
			desired[controller.ChannelKey(ch.Kind, ch.Name)] = ch
		}
	}

	for key, ch := range desired {
		tokens, hash, err := s.resolveTokens(ch)
		if err != nil {
			s.logger.Warn("channel supervisor: resolve secret", "channel", key, "error", err)
			continue
		}
		if err := s.warmer.Warmup(ch.Bindings); err != nil {
			s.logger.Warn("channel supervisor: warmup capability settings", "channel", key, "error", err)
			continue
		}
		s.mu.Lock()
		current, ok := s.running[key]
		s.mu.Unlock()
		switch {
		case ok && current.tokenHash == hash:
			current.setPolicies(ch.Bindings) // routing-only change: hot-swap
		case ok:
			s.stop(key, true) // token changed: restart cleanly
			s.startChannel(key, ch, tokens, hash)
		default:
			s.startChannel(key, ch, tokens, hash)
		}
	}

	s.mu.Lock()
	stale := make([]string, 0)
	for key := range s.running {
		if _, ok := desired[key]; !ok {
			stale = append(stale, key)
		}
	}
	s.mu.Unlock()
	for _, key := range stale {
		s.stop(key, false)
	}
}

// resolveTokens decrypts a channel's credential sources and returns them keyed by
// field plus a content hash used to detect token changes.
func (s *Supervisor) resolveTokens(ch controller.ResolvedChannel) (map[string][]byte, string, error) {
	out := make(map[string][]byte, len(ch.Secrets))
	h := sha256.New()
	for _, field := range []string{"appToken", "botToken"} {
		src, ok := ch.Secrets[field]
		if !ok {
			continue
		}
		val, err := s.resolver.Resolve(src)
		if err != nil {
			return nil, "", err
		}
		out[field] = val
		h.Write([]byte(field))
		h.Write(val)
		h.Write([]byte{0})
	}
	return out, hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Supervisor) startChannel(key string, ch controller.ResolvedChannel, tokens map[string][]byte, hash string) {
	start, ok := s.starters[ch.Source]
	if !ok {
		return
	}
	m, err := start(ch, tokens, hash)
	if err != nil {
		s.logger.Warn("channel supervisor: start bridge", "channel", key, "error", err)
		return
	}
	s.mu.Lock()
	s.running[key] = m
	s.mu.Unlock()
	s.logger.Info("channel supervisor: bridge started", "channel", key, "source", ch.Source)
}

func (s *Supervisor) startTelegram(ch controller.ResolvedChannel, tokens map[string][]byte, hash string) (*managed, error) {
	parent := s.parentCtx()
	client := tgapi.NewClient(string(tokens["botToken"]))
	if s.telegramBaseURL != "" {
		client.SetBaseURL(s.telegramBaseURL)
	}
	identity, err := client.GetMe(parent)
	if err != nil {
		return nil, err
	}
	store, err := tgstate.Open(filepath.Join(s.dataDir, "telegram-"+fileSafe(ch.Name)+".db"), s.stateKey)
	if err != nil {
		return nil, err
	}
	if len(ch.Bindings) == 0 {
		store.Close()
		return nil, fmt.Errorf("no bindings are ready for this channel — check binding validation warnings above")
	}
	policies, err := tgpolicy.FromResolved(ch.Bindings)
	if err != nil {
		store.Close()
		return nil, err
	}
	svc := tgchat.New(s.runtime, client, store, policies, identity, s.logger)
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go s.run(ctx, "telegram/"+ch.Name, svc, done)
	return &managed{
		tokenHash: hash, cancel: cancel, done: done, closeStore: store.Close,
		setPolicies: func(b []binding.Resolved) {
			if p, err := tgpolicy.FromResolved(b); err != nil {
				s.logger.Warn("channel supervisor: reroute", "channel", "telegram/"+ch.Name, "error", err)
			} else {
				svc.SetPolicies(p)
			}
		},
	}, nil
}

func (s *Supervisor) startSlack(ch controller.ResolvedChannel, tokens map[string][]byte, hash string) (*managed, error) {
	parent := s.parentCtx()
	client, err := slapi.NewClient(string(tokens["appToken"]), string(tokens["botToken"]))
	if err != nil {
		return nil, err
	}
	store, err := slstate.Open(filepath.Join(s.dataDir, "slack-"+fileSafe(ch.Name)+".db"), s.stateKey)
	if err != nil {
		return nil, err
	}
	if len(ch.Bindings) == 0 {
		store.Close()
		return nil, fmt.Errorf("no bindings are ready for this channel — check binding validation warnings above")
	}
	policies, err := slpolicy.FromResolved(ch.Bindings)
	if err != nil {
		store.Close()
		return nil, err
	}
	svc := slchat.New(s.runtime, client, store, policies, s.logger)
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go s.run(ctx, "slack/"+ch.Name, svc, done)
	return &managed{
		tokenHash: hash, cancel: cancel, done: done, closeStore: store.Close,
		setPolicies: func(b []binding.Resolved) {
			if p, err := slpolicy.FromResolved(b); err != nil {
				s.logger.Warn("channel supervisor: reroute", "channel", "slack/"+ch.Name, "error", err)
			} else {
				svc.SetPolicies(p)
			}
		},
	}, nil
}

// bridge is the slice of a chat service the supervisor drives.
type bridge interface {
	Start(ctx context.Context) error
}

func (s *Supervisor) run(ctx context.Context, name string, b bridge, done chan struct{}) {
	defer close(done)
	if err := b.Start(ctx); err != nil && ctx.Err() == nil {
		s.logger.Error("channel supervisor: bridge exited", "channel", name, "error", err)
	}
}

// stop cancels a bridge and removes it. When wait is true it blocks until the
// bridge goroutine has exited and its store is closed (used before a restart so
// the per-channel sqlite file is free); otherwise it closes the store in the
// background.
func (s *Supervisor) stop(key string, wait bool) {
	s.mu.Lock()
	m, ok := s.running[key]
	if ok {
		delete(s.running, key)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	m.cancel()
	closeStore := func() {
		<-m.done
		if m.closeStore != nil {
			_ = m.closeStore()
		}
	}
	if wait {
		closeStore()
	} else {
		go closeStore()
		s.logger.Info("channel supervisor: bridge stopped", "channel", key)
	}
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	keys := make([]string, 0, len(s.running))
	for key := range s.running {
		keys = append(keys, key)
	}
	s.mu.Unlock()
	for _, key := range keys {
		s.stop(key, true)
	}
}

// fileSafe turns a Manifest-scoped channel name (manifestName/channelName) into a
// flat, filesystem-safe token for per-channel sqlite filenames.
func fileSafe(name string) string { return strings.ReplaceAll(name, "/", "-") }

func (s *Supervisor) parentCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
