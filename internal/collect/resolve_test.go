package collect

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDNSResolverConcurrencyAndTimeout(t *testing.T) {
	var cur, max int32
	var mu sync.Mutex
	r := DNSResolver{
		Name:    "fake",
		Workers: 3,
		Timeout: 50 * time.Millisecond,
		Lookup: func(ctx context.Context, host string) ([]string, error) {
			n := atomic.AddInt32(&cur, 1)
			mu.Lock()
			if n > max {
				max = n
			}
			mu.Unlock()
			defer atomic.AddInt32(&cur, -1)
			switch host {
			case "fail.example":
				return nil, errors.New("nxdomain")
			case "empty.example":
				return nil, nil
			case "slow.example":
				<-ctx.Done()
				return nil, ctx.Err()
			}
			time.Sleep(5 * time.Millisecond)
			return []string{"203.0.113.1"}, nil
		},
	}
	domains := []string{"z.example", "fail.example", "a.example", "empty.example", "slow.example", "m.example", "b.example", "c.example"}
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	_ = now
	out := r.Resolve(context.Background(), domains)
	if max > 3 {
		t.Errorf("max concurrency %d", max)
	}
	var got []string
	for _, x := range out {
		got = append(got, x.Domain)
		if x.Source != "outflow" || x.Resolver != "fake" || x.At.IsZero() {
			t.Errorf("bad resolution %+v", x)
		}
	}
	want := []string{"a.example", "b.example", "c.example", "m.example", "z.example"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestNewDNSResolverDefaults(t *testing.T) {
	r := NewDNSResolver()
	if r.Name != "system resolver" || r.Workers != 16 || r.Timeout != 3*time.Second || r.Lookup == nil {
		t.Errorf("%+v", r)
	}
}
