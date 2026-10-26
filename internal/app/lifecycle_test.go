package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/invisi-lab/invisi-protocol-rpc/internal/config"
)

func launchReloadService(t *testing.T, cfg config.Config, dir string) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() { defer close(exited); done <- runWithInterval(ctx, cfg, dir, 10*time.Millisecond) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("reload service did not stop")
		}
	})
	return cancel, done
}

func waitDiscoveryIDs(t *testing.T, listen string, ids ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + listen + "/v1")
		if err == nil {
			var body struct {
				APIVersion string           `json:"apiVersion"`
				Chains     []discoveryChain `json:"chains"`
			}
			err = json.NewDecoder(response.Body).Decode(&body)
			response.Body.Close()
			match := err == nil && response.StatusCode == 200 && body.APIVersion == "v1" && body.Chains != nil && len(body.Chains) == len(ids)
			if match {
				for i, id := range ids {
					if body.Chains[i].ChainID != id || body.Chains[i].ProtocolVersion != "1" {
						match = false
					}
				}
			}
			if match {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("discovery did not reach %v", ids)
}
func updateServiceConfig(t *testing.T, dir string, fn func(*config.Config)) {
	t.Helper()
	if err := config.Update(context.Background(), dir, func(cfg *config.Config) error { fn(cfg); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeReloadAddRemoveReaddAndRPCReplacement(t *testing.T) {
	cfg, a, _ := twoChainService(t)
	for i := range cfg.Chains {
		cfg.Chains[i].SnapshotInterval = "0s"
	}
	chains := append([]config.Chain(nil), cfg.Chains...)
	cfg.Chains = nil
	dir := t.TempDir()
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	_, done := launchReloadService(t, cfg, dir)
	waitDiscoveryIDs(t, cfg.Listen)
	updateServiceConfig(t, dir, func(c *config.Config) { c.Chains = chains })
	waitDiscoveryIDs(t, cfg.Listen, "31338", "31337")
	waitChainStatus(t, cfg.Listen, "31337", 0, done)
	a.advanceTo.Store(2)
	waitChainStatus(t, cfg.Listen, "31337", 2, done)
	// A rejected replacement must keep the former RPC and route working.
	wrong := &chainRPCFixture{chainID: 99}
	_, wrongURL := fixtureChain(t, wrong)
	updateServiceConfig(t, dir, func(c *config.Config) { c.Chains[1].ChainRPC = wrongURL })
	deadline := time.Now().Add(3 * time.Second)
	for wrong.chainIDCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if wrong.chainIDCalls.Load() == 0 {
		t.Fatal("wrong-chain candidate never preflighted")
	}
	time.Sleep(50 * time.Millisecond)
	a.advanceTo.Store(3)
	waitChainStatus(t, cfg.Listen, "31337", 3, done)
	// A valid replacement is adopted and begins indexing the new upstream.
	replacement := &chainRPCFixture{chainID: 31337, protocol: chains[1].Deployment.Protocol, latest: 4}
	_, replacementURL := fixtureChain(t, replacement)
	updateServiceConfig(t, dir, func(c *config.Config) { c.Chains[1].ChainRPC = replacementURL })
	waitChainStatus(t, cfg.Listen, "31337", 4, done)
	if replacement.versionCalls.Load() == 0 {
		t.Fatal("replacement was not preflighted")
	}
	updateServiceConfig(t, dir, func(c *config.Config) { c.Chains = c.Chains[:1] })
	waitDiscoveryIDs(t, cfg.Listen, "31338")
	waitChainStatus(t, cfg.Listen, "31338", 0, done)
	response, err := http.Get("http://" + cfg.Listen + "/v1/chains/31337")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("removed route status %d", response.StatusCode)
	}
	// Data survives removal; even the original upstream behind the saved checkpoint
	// cannot replace it, so re-add the valid replacement and inspect saved height.
	updateServiceConfig(t, dir, func(c *config.Config) {
		chain := chains[1]
		chain.ChainRPC = replacementURL
		c.Chains = append(c.Chains, chain)
	})
	waitDiscoveryIDs(t, cfg.Listen, "31338", "31337")
	waitChainStatus(t, cfg.Listen, "31337", 4, done)
	updateServiceConfig(t, dir, func(c *config.Config) { c.Chains = nil; c.Listen = "127.0.0.1:1" })
	waitDiscoveryIDs(t, cfg.Listen)
}

func TestRuntimeBlockedCandidateDoesNotBlockRemovalOrShutdown(t *testing.T) {
	cfg, _, _ := twoChainService(t)
	cfg.Chains = cfg.Chains[:1]
	cfg.Chains[0].SnapshotInterval = "0s"
	dir := t.TempDir()
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	cancel, done := launchReloadService(t, cfg, dir)
	waitDiscoveryIDs(t, cfg.Listen, "31338")
	blocked := &chainRPCFixture{chainID: 31337, awaitLatest: make(chan struct{}, 1)}
	_, url := fixtureChain(t, blocked)
	chain := serviceConfig(t)
	chain.ChainRPC = url
	chain.Deployment.FromBlock = 0
	updateServiceConfig(t, dir, func(c *config.Config) { c.Chains = append(c.Chains, chain) })
	select {
	case <-blocked.awaitLatest:
	case <-time.After(3 * time.Second):
		t.Fatal("candidate never started")
	}
	updateServiceConfig(t, dir, func(c *config.Config) { c.Chains = c.Chains[1:] })
	waitDiscoveryIDs(t, cfg.Listen)
	// Removing a blocked candidate cancels its HTTP preflight and releases its DB.
	updateServiceConfig(t, dir, func(c *config.Config) { c.Chains = nil })
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown waited for blocked candidate timeout")
	}
	store, err := OpenDatabase(context.Background(), chain)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
}

func TestRuntimeWithdrawalCancelsAndDrainsRequestBeforeClose(t *testing.T) {
	chain := serviceConfig(t)
	store, err := OpenDatabase(context.Background(), chain)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := fixtureChain(t, &chainRPCFixture{})
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	runtime := &chainRuntime{config: chain, store: store, client: client, handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
		<-release
		if _, err := store.Checkpoint(context.Background()); err != nil {
			t.Errorf("store closed before request lease drained: %v", err)
		}
	})}
	live := newLiveChain(context.Background(), runtime)
	close(live.workersDone)
	router := &liveRouter{chains: map[string]*liveChain{chain.Deployment.ChainID: live}}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chains/"+chain.Deployment.ChainID, nil))
	}()
	<-entered
	router.mu.Lock()
	delete(router.chains, chain.Deployment.ChainID)
	live.cancel()
	router.mu.Unlock()
	go live.retire()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("withdrawal did not cancel request")
	}
	select {
	case <-live.closed:
		t.Fatal("closed before request drained")
	default:
	}
	absent := httptest.NewRecorder()
	router.ServeHTTP(absent, httptest.NewRequest(http.MethodPost, "/v1/chains/"+chain.Deployment.ChainID, nil))
	if absent.Code != 404 {
		t.Fatalf("withdrawn route status %d", absent.Code)
	}
	close(release)
	select {
	case <-live.closed:
	case <-time.After(time.Second):
		t.Fatal("retirement did not finish after lease drained")
	}
	<-requestDone
}
