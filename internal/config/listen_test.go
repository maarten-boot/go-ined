package config

import "testing"

// TC-801: loopback detection and the token requirement.
func TestListenSecurity(t *testing.T) {
	cases := []struct {
		listen, token string
		ok            bool
	}{
		{"127.0.0.1:8470", "", true},
		{"[::1]:8470", "", true},
		{"localhost:8470", "", true},
		{"127.0.0.2:8470", "", true},
		{":8470", "", false},
		{"0.0.0.0:8470", "", false},
		{"[::]:8470", "", false},
		{"192.0.2.1:8470", "", false},
		{"myhost:8470", "", false},
		{"0.0.0.0:8470", "secret", true},
		{"no-port", "", false},
	}
	for _, c := range cases {
		cfg := DefaultServer()
		cfg.Server.Listen, cfg.Server.Token = c.listen, c.token
		if err := CheckListenSecurity(cfg); (err == nil) != c.ok {
			t.Errorf("%q token=%q: err=%v, want ok=%v", c.listen, c.token, err, c.ok)
		}
	}
}
