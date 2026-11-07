package collect

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/jbrathwa/outflow/internal/model"
)

// DNSResolver resolves domains with a bounded worker pool. Failed or empty lookups are omitted.
type DNSResolver struct {
	Lookup  func(ctx context.Context, host string) ([]string, error)
	Name    string
	Workers int
	Timeout time.Duration
}

// NewDNSResolver returns a resolver backed by the system resolver.
func NewDNSResolver() DNSResolver {
	return DNSResolver{
		Lookup:  net.DefaultResolver.LookupHost,
		Name:    "system resolver",
		Workers: 16,
		Timeout: 3 * time.Second,
	}
}

// Resolve implements Resolver. Results are sorted by domain.
func (r DNSResolver) Resolve(ctx context.Context, domains []string) []model.Resolution {
	workers := r.Workers
	if workers < 1 {
		workers = 1
	}
	now := time.Now().UTC()
	jobs := make(chan string)
	var mu sync.Mutex
	var out []model.Resolution
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range jobs {
				lctx, cancel := ctx, context.CancelFunc(func() {})
				if r.Timeout > 0 {
					lctx, cancel = context.WithTimeout(ctx, r.Timeout)
				}
				ips, err := r.Lookup(lctx, d)
				cancel()
				if err != nil || len(ips) == 0 {
					continue
				}
				mu.Lock()
				out = append(out, model.Resolution{Domain: d, IPs: ips, Source: "outflow", Resolver: r.Name, At: now})
				mu.Unlock()
			}
		}()
	}
	for _, d := range domains {
		jobs <- d
	}
	close(jobs)
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}
