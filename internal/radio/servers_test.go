package radio

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
)

// fakeResolver answers from canned records.
type fakeResolver struct {
	srv     []*net.SRV
	srvErr  error
	hosts   []string
	hostErr error
	names   map[string][]string
}

func (f fakeResolver) LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
	return "", f.srv, f.srvErr
}

func (f fakeResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if host != "all.api.radio-browser.info" {
		return nil, errors.New("unexpected host " + host)
	}
	return f.hosts, f.hostErr
}

func (f fakeResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	n, ok := f.names[addr]
	if !ok {
		return nil, errors.New("no name")
	}
	return n, nil
}

func reverse(s []string) { slices.Reverse(s) }

func TestServersFromSRV(t *testing.T) {
	r := fakeResolver{srv: []*net.SRV{{Target: "de1.api.radio-browser.info."}, {Target: "nl1.api.radio-browser.info."}}}
	got, err := Servers(context.Background(), r, reverse)
	want := []string{"https://nl1.api.radio-browser.info", "https://de1.api.radio-browser.info"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Servers = %v, %v, want %v", got, err, want)
	}
}

func TestServersFallBackToAddresses(t *testing.T) {
	r := fakeResolver{
		srvErr: errors.New("no srv"),
		hosts:  []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4"},
		names: map[string][]string{
			"192.0.2.1": {"de1.api.radio-browser.info."},
			"192.0.2.2": {"nl1.api.radio-browser.info."},
			"192.0.2.3": {"de1.api.radio-browser.info."},
		},
	}
	got, err := Servers(context.Background(), r, reverse)
	want := []string{"https://nl1.api.radio-browser.info", "https://de1.api.radio-browser.info"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Servers = %v, %v, want %v", got, err, want)
	}
}

func TestServersFindNothing(t *testing.T) {
	r := fakeResolver{srvErr: errors.New("no srv"), hostErr: errors.New("no dns")}
	if _, err := Servers(context.Background(), r, reverse); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Servers = %v, want ErrUnreachable", err)
	}
	r = fakeResolver{srvErr: errors.New("no srv"), hosts: []string{"192.0.2.1"}}
	if _, err := Servers(context.Background(), r, reverse); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Servers without names = %v, want ErrUnreachable", err)
	}
}
