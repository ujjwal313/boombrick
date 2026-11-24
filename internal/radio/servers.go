package radio

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
)

// ErrUnreachable means no Radio Browser server could be found or none
// answered.
var ErrUnreachable = errors.New("radio: no Radio Browser server answers")

// Resolver looks up DNS records. *net.Resolver is one.
type Resolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// Servers finds Radio Browser's API servers, as Radio Browser asks apps to:
// from the DNS SRV record _api._tcp.radio-browser.info, or when that fails
// from the addresses of all.api.radio-browser.info and their names. They come
// back in shuffle's order as base URLs. No server is written into the code.
func Servers(ctx context.Context, r Resolver, shuffle func([]string)) ([]string, error) {
	var hosts []string
	if _, srvs, err := r.LookupSRV(ctx, "api", "tcp", "radio-browser.info"); err == nil {
		for _, s := range srvs {
			hosts = append(hosts, strings.TrimSuffix(s.Target, "."))
		}
	}
	if len(hosts) == 0 {
		addrs, err := r.LookupHost(ctx, "all.api.radio-browser.info")
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
		for _, a := range addrs {
			if names, err := r.LookupAddr(ctx, a); err == nil && len(names) > 0 {
				hosts = append(hosts, strings.TrimSuffix(names[0], "."))
			}
		}
	}
	slices.Sort(hosts)
	hosts = slices.Compact(hosts)
	if len(hosts) == 0 {
		return nil, ErrUnreachable
	}
	shuffle(hosts)
	urls := make([]string, len(hosts))
	for i, h := range hosts {
		urls[i] = "https://" + h
	}
	return urls, nil
}
