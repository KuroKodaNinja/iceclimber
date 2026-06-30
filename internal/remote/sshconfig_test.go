package remote

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestParseSSHGOutput(t *testing.T) {
	// A realistic `ssh -G` dump: lowercased keys, a repeated identityfile (one a
	// nonexistent default), proxyjump set, proxycommand "none", and a two-path
	// userknownhostsfile. Keys we don't care about are ignored.
	out := `host sandbox
hostname 10.0.2.15
port 2222
user agent
identityfile ~/.ssh/id_ed25519
identityfile ~/.ssh/id_rsa
proxyjump bastion.corp:2200
proxycommand none
userknownhostsfile ~/.ssh/known_hosts ~/.ssh/known_hosts2
forwardagent no`
	got := parseSSHGOutput(strings.NewReader(out))
	want := &resolvedSSH{
		HostName:        "10.0.2.15",
		Port:            2222,
		User:            "agent",
		IdentityFiles:   []string{"~/.ssh/id_ed25519", "~/.ssh/id_rsa"},
		ProxyJump:       "bastion.corp:2200",
		KnownHostsFiles: []string{"~/.ssh/known_hosts", "~/.ssh/known_hosts2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSSHGOutput =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestParseSSHGOutput_NoneAndEmpty(t *testing.T) {
	// "none"/empty proxy + known-hosts values are absent, not literal strings.
	got := parseSSHGOutput(strings.NewReader("hostname h\nproxyjump none\nproxycommand none\nuserknownhostsfile none\n"))
	if got.ProxyJump != "" || got.ProxyCommand != "" || len(got.KnownHostsFiles) != 0 {
		t.Errorf("none/empty not treated as absent: %+v", got)
	}
}

func TestProxyArgv(t *testing.T) {
	const ssh = "/usr/bin/ssh"
	tests := []struct {
		name string
		r    resolvedSSH
		want []string
	}{
		{
			name: "direct (no proxy)",
			r:    resolvedSSH{HostName: "h", Port: 22},
			want: nil,
		},
		{
			name: "single jump",
			r:    resolvedSSH{HostName: "10.0.2.15", Port: 22, ProxyJump: "bastion"},
			want: []string{ssh, "-W", "10.0.2.15:22", "bastion"},
		},
		{
			name: "multi-hop jump",
			r:    resolvedSSH{HostName: "10.0.2.15", Port: 2222, ProxyJump: "j1,j2,j3"},
			want: []string{ssh, "-J", "j1,j2", "-W", "10.0.2.15:2222", "j3"},
		},
		{
			name: "explicit ProxyCommand (and ProxyJump precedence is moot when only cmd set)",
			r:    resolvedSSH{HostName: "h", Port: 22, ProxyCommand: "corkscrew proxy 8080 %h %p"},
			want: []string{"/bin/sh", "-c", "corkscrew proxy 8080 %h %p"},
		},
		{
			name: "ProxyJump wins over ProxyCommand",
			r:    resolvedSSH{HostName: "h", Port: 22, ProxyJump: "b", ProxyCommand: "should-be-ignored"},
			want: []string{ssh, "-W", "h:22", "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.proxyArgv(ssh, ""); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("proxyArgv = %q, want %q", got, tt.want)
			}
		})
	}

	// configFile is propagated as -F so a jump alias resolves against it (only in
	// the ProxyJump branch; an explicit ProxyCommand is literal).
	r := resolvedSSH{HostName: "h", Port: 22, ProxyJump: "b"}
	if got := r.proxyArgv(ssh, "/tmp/cfg"); !reflect.DeepEqual(got, []string{ssh, "-F", "/tmp/cfg", "-W", "h:22", "b"}) {
		t.Errorf("proxyArgv with -F = %q", got)
	}
}

func TestResolveInputArgs(t *testing.T) {
	in := resolveInput{Alias: "sandbox", Port: 2222, User: "agent", IdentityFile: "/k", KnownHosts: "/kh", ConfigFile: "/cfg"}
	want := []string{"-G", "-F", "/cfg", "-p", "2222", "-l", "agent", "-o", "IdentityFile=/k", "-o", "UserKnownHostsFile=/kh", "sandbox"}
	if got := in.args(); !reflect.DeepEqual(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}
	// Bare alias: no overrides, just -G <alias>.
	if got := (resolveInput{Alias: "h"}).args(); !reflect.DeepEqual(got, []string{"-G", "h"}) {
		t.Errorf("bare args = %q", got)
	}
}

// TestResolveSSHConfig_Fake drives resolveSSHConfig with a stubbed ssh -G runner
// (no real ssh client), proving it parses what the binary returns. Not parallel:
// it swaps the package-level runSSHG.
func TestResolveSSHConfig_Fake(t *testing.T) {
	if _, err := sshBinary(); err != nil {
		t.Skip("no ssh client to satisfy sshBinary(); parse logic covered elsewhere")
	}
	orig := runSSHG
	t.Cleanup(func() { runSSHG = orig })
	var gotArgs []string
	runSSHG = func(_ context.Context, _ string, args []string) ([]byte, error) {
		gotArgs = args
		return []byte("hostname 1.2.3.4\nport 22\nuser bob\nproxyjump bastion\n"), nil
	}
	r, err := resolveSSHConfig(context.Background(), resolveInput{Alias: "sandbox", User: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if r.HostName != "1.2.3.4" || r.User != "bob" || r.ProxyJump != "bastion" {
		t.Errorf("resolved = %+v", r)
	}
	if !reflect.DeepEqual(gotArgs, []string{"-G", "-l", "bob", "sandbox"}) {
		t.Errorf("ssh -G args = %q", gotArgs)
	}
}
