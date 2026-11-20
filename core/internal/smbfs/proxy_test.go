package smbfs

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/C2-POLY/cloud-os/core/internal/auth"
	"github.com/C2-POLY/cloud-os/core/internal/edge"
)

// serveBehindProxy starts the smb side as the box runs it behind the
// tailscale service: loopback stands for the proxy, which sends a PROXY
// header naming each device.
func serveBehindProxy(t *testing.T, fx *fixture) *Server {
	t.Helper()
	proxies, err := edge.ParseProxies("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv, err := Start(ctx, Config{Addr: "127.0.0.1:0", Auth: auth.NewService(fx.store.Pool), Store: fx.store, Proxies: proxies, loginDelay: time.Millisecond})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// dialAs signs in through the proxy, as the device at src.
func dialAs(t *testing.T, addr, src, user, password string) (*smb2.Session, error) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, "PROXY TCP4 "+src+" 100.100.9.9 40000 445\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: user, Password: password}}
	s, err := d.Dial(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	t.Cleanup(func() { _ = s.Logoff() })
	return s, nil
}

func TestSMBBehindTheTailscaleProxy(t *testing.T) {
	fx := newFixture(t)
	addr := serveBehindProxy(t, fx).Addr().String()

	// A device of the tailnet reads its drive.
	s, err := dialAs(t, addr, "100.101.0.5", testUser, fx.passwords[testUser])
	if err != nil {
		t.Fatalf("tailnet device: %v", err)
	}
	share, err := s.Mount(ShareName)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := share.ReadFile(`docs\a.txt`); err != nil || string(b) != string(fx.files["docs/a.txt"]) {
		t.Errorf("read: %q %v", b, err)
	}
	_ = share.Umount()

	// A public address in the header is refused: Funnel never opens SMB.
	if _, err := dialAs(t, addr, "203.0.113.5", testUser, fx.passwords[testUser]); err == nil {
		t.Error("a public address came in through the proxy")
	}

	// The attempt limit counts each device apart.
	for range 5 {
		if _, err := dialAs(t, addr, "100.101.0.6", testUser, "wrong"); err == nil {
			t.Fatal("a wrong password signed in")
		}
	}
	if _, err := dialAs(t, addr, "100.101.0.6", testUser, fx.passwords[testUser]); err == nil {
		t.Error("the device that failed five times was not held back")
	}
	if _, err := dialAs(t, addr, "100.101.0.7", testUser, fx.passwords[testUser]); err != nil {
		t.Errorf("another device was held back: %v", err)
	}
}

func TestAllowed(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "192.168.1.2", "10.0.0.1", "172.18.0.9", "100.64.0.1", "100.127.255.254", "fd7a:115c:a1e0::1", "fe80::1"} {
		if !allowed(net.ParseIP(ip)) {
			t.Errorf("%s refused", ip)
		}
	}
	for _, ip := range []string{"203.0.113.5", "100.63.255.255", "100.128.0.1", "8.8.8.8", "2001:db8::1"} {
		if allowed(net.ParseIP(ip)) {
			t.Errorf("%s allowed", ip)
		}
	}
}
