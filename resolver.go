package k8sresolver

import (
	"context"
	"github.com/rs/zerolog/log"
	"go.guoyk.net/k8sresolver/pkg/k8s"
	"google.golang.org/grpc/resolver"
	"time"
)

var (
	// RefreshInterval periodic refresh interval
	RefreshInterval = time.Minute

	// DebounceInterval debounce interval
	DebounceInterval = time.Second * 3
)

type Resolver struct {
	target k8s.Target
	conn   resolver.ClientConn
	client k8s.Client

	cancel   context.CancelFunc
	resolves chan interface{}
	results  chan []string
}

func NewResolver(target k8s.Target, cc resolver.ClientConn, _ resolver.BuildOption, client k8s.Client) *Resolver {
	r := &Resolver{
		target:   target,
		conn:     cc,
		client:   client,
		resolves: make(chan interface{}, 1),
		results:  make(chan []string, 1),
	}
	return r
}

func (r *Resolver) Start() {
	if r.cancel != nil {
		return
	}
	var ctx context.Context
	ctx, r.cancel = context.WithCancel(context.Background())
	go r.run(ctx)
}

func (r *Resolver) runPeriodicResolve(ctx context.Context) {
	// periodical adjective resolves
	tk := time.NewTicker(RefreshInterval)
	defer tk.Stop()

	for {
		select {
		case <-tk.C:
			log.Debug().Msg("k8s resolver: timer ticked")
			r.resolveNow()
		case <-ctx.Done():
			return
		}
	}
}

func (r *Resolver) runResolveExecutor(ctx context.Context) {
	debounce(ctx, DebounceInterval, r.resolves, func() {
		if addrs, err := r.client.GetAddresses(ctx, r.target); err != nil {
			log.Error().Err(err).Msg("k8s resolver: update request failed")
			return
		} else {
			log.Debug().Strs("addrs", addrs).Msg("k8s resolver: update request succeeded")
			r.results <- addrs
		}
	})
}

func (r *Resolver) runPassiveResolve(ctx context.Context) {
	r.client.WatchAddress(ctx, r.target, r.results)
}

func (r *Resolver) run(ctx context.Context) {
	go r.runResolveExecutor(ctx)
	go r.runPeriodicResolve(ctx)
	go r.runPassiveResolve(ctx)

	// initial resolve
	r.resolveNow()

	// apply
	var last []string
	for {
		select {
		case addrs := <-r.results:
			if strSliceEqual(addrs, last) {
				log.Debug().Msg("k8s resolver: addresses no change")
				continue
			}
			log.Debug().Strs("addrs", addrs).Msg("k8s resolver: new addresses")
			// build grpc state and apply
			state := resolver.State{}
			for _, addr := range addrs {
				state.Addresses = append(state.Addresses, resolver.Address{Addr: addr, Type: resolver.Backend})
			}
			r.conn.UpdateState(state)
			// record last
			last = addrs
		case <-ctx.Done():
			// on closed
			return
		}
	}
}

func (r *Resolver) resolveNow() {
	r.resolves <- nil
}

func (r *Resolver) ResolveNow(opt resolver.ResolveNowOption) {
	log.Debug().Interface("opt", opt).Msg("k8s resolver: gRPC asked for resolving now")
	go r.resolveNow()
}

func (r *Resolver) Close() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
}
