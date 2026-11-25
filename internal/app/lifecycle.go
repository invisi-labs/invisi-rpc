package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/invisi-labs/invisi-rpc/internal/config"
	"github.com/invisi-labs/invisi-rpc/internal/protocol"
	apirpc "github.com/invisi-labs/invisi-rpc/internal/rpc"
)

// liveChain owns its client, database, workers and requests. Route acquisition and
// withdrawal share router.mu, so no request can acquire a lease after retirement.
type liveChain struct {
	runtime     *chainRuntime
	ctx         context.Context
	cancel      context.CancelFunc
	requests    sync.WaitGroup
	workers     sync.WaitGroup
	workersDone chan struct{}
	closed      chan struct{}
}

func newLiveChain(ctx context.Context, runtime *chainRuntime) *liveChain {
	ctx, cancel := context.WithCancel(ctx)
	return &liveChain{runtime: runtime, ctx: ctx, cancel: cancel, workersDone: make(chan struct{}), closed: make(chan struct{})}
}

func (c *liveChain) start(dir string, previous *liveChain) {
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		if previous != nil {
			select {
			case <-previous.workersDone:
			case <-c.ctx.Done():
				return
			}
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); c.runtime.runIndex(c.ctx) }()
		interval, _ := time.ParseDuration(c.runtime.config.SnapshotInterval)
		if interval > 0 {
			wg.Add(1)
			go func() { defer wg.Done(); c.runtime.runSnapshots(c.ctx, interval, dir) }()
		}
		wg.Wait()
	}()
	go func() { c.workers.Wait(); close(c.workersDone) }()
}

func (c *liveChain) retire() {
	c.cancel()
	<-c.workersDone
	c.requests.Wait()
	c.runtime.store.Close()
	c.runtime.client.Close()
	close(c.closed)
}

func closeRuntime(c *chainRuntime) { c.store.Close(); c.client.Close() }

type liveRouter struct {
	mu     sync.Mutex
	chains map[string]*liveChain
	order  []string
}

func (router *liveRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	router.mu.Lock()
	if r.URL.Path == protocol.APIPrefix || r.URL.Path == protocol.APIPrefix+"/" {
		discovery := struct {
			APIVersion string           `json:"apiVersion"`
			Chains     []discoveryChain `json:"chains"`
		}{protocol.APIVersion, make([]discoveryChain, 0, len(router.chains))}
		for _, id := range router.order {
			if c := router.chains[id]; c != nil {
				discovery.Chains = append(discovery.Chains, discoveryChain{id, protocol.APIPrefix + "/chains/" + id, c.runtime.protocolVersion})
			}
		}
		router.mu.Unlock()
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			apirpc.WriteHTTPError(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(discovery)
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	id := strings.TrimPrefix(path, protocol.APIPrefix+"/chains/")
	c := router.chains[id]
	if c == nil || path != protocol.APIPrefix+"/chains/"+id {
		router.mu.Unlock()
		apirpc.WriteHTTPError(w, http.StatusNotFound, "route not found")
		return
	}
	c.requests.Add(1)
	router.mu.Unlock()
	defer c.requests.Done()
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	if c.ctx.Err() != nil {
		cancel()
	}
	c.runtime.handler.ServeHTTP(w, r.WithContext(ctx))
}

type chainSlot struct {
	desired    config.Chain
	generation uint64
	cancel     context.CancelFunc
	active     *liveChain
	retiring   *liveChain
	retry      time.Time
	failures   uint
}
type candidateResult struct {
	id         string
	generation uint64
	runtime    *chainRuntime
	err        error
}

func runWithInterval(ctx context.Context, cfg config.Config, dir string, interval time.Duration) error {
	cfg.Chains = append([]config.Chain(nil), cfg.Chains...)
	if err := cfg.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	router := &liveRouter{chains: map[string]*liveChain{}}
	slots := map[string]*chainSlot{}
	var jobs sync.WaitGroup
	// Only this goroutine adds jobs. Shutdown starts waiting after the event loop
	// exits, so Add cannot race a Wait on an empty group.
	defer func() {
		cancel()
		router.mu.Lock()
		for id, c := range router.chains {
			delete(router.chains, id)
			jobs.Add(1)
			go func(c *liveChain) { defer jobs.Done(); c.retire() }(c)
		}
		router.mu.Unlock()
		jobs.Wait()
	}()
	var initial []*chainRuntime
	defer func() {
		for _, runtime := range initial {
			closeRuntime(runtime)
		}
	}()
	for _, chain := range cfg.Chains {
		if ctx.Err() != nil {
			return nil
		}
		runtime, err := initializeChain(ctx, chain)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("initialize chain %s: %w", chain.Deployment.ChainID, err)
		}
		initial = append(initial, runtime)
	}
	if ctx.Err() != nil {
		return nil
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	for _, runtime := range initial {
		live := newLiveChain(ctx, runtime)
		live.start(dir, nil)
		id := runtime.config.Deployment.ChainID
		slots[id] = &chainSlot{desired: runtime.config, active: live}
		router.chains[id] = live
		router.order = append(router.order, id)
	}
	initial = nil // ownership transferred to the live router
	rateHandler, err := apirpc.LimitIPRequests(router, cfg.RateLimit)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Listen, Handler: apirpc.LimitConcurrentRequests(rateHandler, cfg.MaxConcurrentRequests), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	errs := make(chan error, 1)
	go func() { errs <- server.Serve(listener) }()
	defer func() {
		cancel()
		shutdown, finish := context.WithTimeout(context.Background(), 5*time.Second)
		defer finish()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
		_ = listener.Close()
	}()
	results := make(chan candidateResult)
	launch := func(id string, slot *chainSlot) {
		candidateCtx, finish := context.WithCancel(ctx)
		slot.cancel = finish
		generation, chain, previous := slot.generation, slot.desired, slot.retiring
		reconcile := slot.active == nil
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			defer finish()
			// Re-addition waits for the former owner to close, without blocking changes
			// to any other chain or shutdown.
			if previous != nil {
				select {
				case <-previous.closed:
				case <-candidateCtx.Done():
					return
				}
			}
			runtime, err := prepareChain(candidateCtx, chain, reconcile)
			if candidateCtx.Err() != nil {
				if runtime != nil {
					closeRuntime(runtime)
				}
				return
			}
			select {
			case results <- candidateResult{id, generation, runtime, err}:
			case <-candidateCtx.Done():
				if runtime != nil {
					closeRuntime(runtime)
				}
			}
		}()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	slog.Info("protocol RPC listening", "listen", listener.Addr().String(), "chains", len(slots))
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errs:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case result := <-results:
			slot := slots[result.id]
			if slot == nil || slot.generation != result.generation {
				if result.runtime != nil {
					closeRuntime(result.runtime)
				}
				continue
			}
			slot.cancel = nil
			if result.err != nil {
				slot.failures++
				delay := time.Second << min(slot.failures-1, 5)
				slot.retry = time.Now().Add(delay)
				slog.Error("chain configuration preflight failed; retaining active chain", "chainId", result.id, "error", result.err, "retry", delay)
				continue
			}
			old := slot.active
			live := newLiveChain(ctx, result.runtime)
			// Register workers before publication/retirement, never Add during Wait.
			live.start(dir, old)
			router.mu.Lock()
			router.chains[result.id] = live
			if old != nil {
				old.cancel()
			}
			router.mu.Unlock()
			slot.active = live
			slot.failures = 0
			if old != nil {
				jobs.Add(1)
				go func() { defer jobs.Done(); old.retire() }()
			}
			slog.Info("protocol chain ready", "chainId", result.id)
		case <-ticker.C:
			next, err := config.LoadDir(dir)
			if err != nil {
				if !os.IsNotExist(err) {
					slog.Error("configuration reload failed; retaining active chains", "error", err)
				}
				continue
			}
			desired := map[string]config.Chain{}
			order := make([]string, 0, len(next.Chains))
			for _, chain := range next.Chains {
				id := chain.Deployment.ChainID
				desired[id] = chain
				order = append(order, id)
			}
			router.mu.Lock()
			router.order = order
			router.mu.Unlock()
			for id, slot := range slots {
				chain, exists := desired[id]
				if exists && reflect.DeepEqual(slot.desired, chain) {
					continue
				}
				if slot.cancel != nil {
					slot.cancel()
					slot.cancel = nil
				}
				slot.generation++
				slot.failures = 0
				slot.retry = time.Time{}
				if !exists {
					if slot.active != nil {
						old := slot.active
						router.mu.Lock()
						delete(router.chains, id)
						old.cancel()
						router.mu.Unlock()
						slot.active = nil
						slot.retiring = old
						jobs.Add(1)
						go func() { defer jobs.Done(); old.retire() }()
					}
					// Keep the tombstone so rapid remove/re-add cannot reuse generations.
					slot.desired = config.Chain{}
				} else {
					slot.desired = chain
				}
			}
			for id, chain := range desired {
				slot := slots[id]
				if slot == nil {
					slot = &chainSlot{desired: chain, generation: 1}
					slots[id] = slot
				}
				if slot.cancel == nil && (slot.active == nil || !reflect.DeepEqual(slot.active.runtime.config, chain)) && !time.Now().Before(slot.retry) {
					launch(id, slot)
				}
			}
		}
	}
}
