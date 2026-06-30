package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// dialPlan is the effective, resolved connection: where to dial (and whether
// through a ProxyCommand subprocess), as whom, verified against which known_hosts.
// It is produced by buildDialPlan and consumed by Dial and FetchHostKey so all
// connection paths agree on the resolved target.
type dialPlan struct {
	host          string   // resolved HostName — the dial target AND the known_hosts key
	port          int
	user          string
	identityFiles []string // explicit key file first, then ssh-config IdentityFiles
	knownHosts    string   // resolved known_hosts path ("" → ~/.ssh/known_hosts)
	proxyArgv     []string // nil = direct dial

	allowPassword bool
	allowKbd      bool
	prompter      PasswordPrompter // nil → ttyPrompter
}

// buildDialPlan resolves cfg into a dialPlan. When UseSSHConfig isn't disabled it
// consults `ssh -G` (authoritative ~/.ssh/config resolution, incl. ProxyJump),
// with explicit cfg fields winning. If no ssh client exists it falls back to a
// literal direct dial — corporate features are simply unavailable, not fatal.
func buildDialPlan(ctx context.Context, cfg DialConfig) (*dialPlan, error) {
	p := &dialPlan{
		host:          cfg.Host,
		port:          cfg.Port,
		user:          cfg.User,
		knownHosts:    cfg.KnownHosts,
		allowPassword: cfg.AllowPassword,
		allowKbd:      cfg.AllowKeyboardInteractive,
		prompter:      cfg.Prompter,
	}
	if cfg.IdentityFile != "" {
		p.identityFiles = []string{cfg.IdentityFile} // explicit key wins (tried first)
	}
	if cfg.UseSSHConfig == nil || *cfg.UseSSHConfig {
		r, err := resolveSSHConfig(ctx, resolveInput{
			Alias:        cfg.Host,
			Port:         cfg.Port,
			User:         cfg.User,
			IdentityFile: cfg.IdentityFile,
			KnownHosts:   cfg.KnownHosts,
			ConfigFile:   cfg.SSHConfigFile,
		})
		switch {
		case err == nil:
			if r.HostName != "" {
				p.host = r.HostName // dial the resolved host, not the alias
			}
			if p.user == "" {
				p.user = r.User
			}
			if p.port == 0 {
				p.port = r.Port
			}
			if p.knownHosts == "" && len(r.KnownHostsFiles) > 0 {
				// ssh -G may emit a ~ path; expand it like we do for identity files
				// (config-provided known_hosts is already expanded by config.Load).
				p.knownHosts = expandTilde(r.KnownHostsFiles[0])
			}
			p.identityFiles = append(p.identityFiles, r.IdentityFiles...) // after the explicit key

			if sshBin, berr := sshBinary(); berr == nil {
				p.proxyArgv = r.proxyArgv(sshBin, cfg.SSHConfigFile)
			}
		case errors.Is(err, errSSHUnavailable):
			// No ssh client. If the operator *explicitly* opted into ssh_config
			// (e.g. they rely on a ProxyJump), fail loud rather than silently
			// direct-dialing the literal host and losing the intended path. With
			// UseSSHConfig unset (default), degrade quietly to a direct dial.
			if cfg.UseSSHConfig != nil && *cfg.UseSSHConfig {
				return nil, fmt.Errorf("use_ssh_config is enabled but no ssh client was found in PATH (needed to resolve %q / any ProxyJump)", cfg.Host)
			}
		default:
			return nil, fmt.Errorf("resolve ssh config for %s: %w", cfg.Host, err)
		}
	}
	if p.port == 0 {
		p.port = 22
	}
	return p, nil
}

// dialTarget opens the byte-stream to the target: directly, or through the
// ProxyCommand subprocess. The returned addr (resolved host:port) is what the
// SSH handshake and the host-key check are keyed on.
func (p *dialPlan) dialTarget(ctx context.Context) (net.Conn, string, error) {
	addr := net.JoinHostPort(p.host, strconv.Itoa(p.port))
	if len(p.proxyArgv) > 0 {
		conn, err := newProxyConn(ctx, p.proxyArgv, addr)
		if err != nil {
			return nil, addr, fmt.Errorf("start proxy for %s: %w", addr, err)
		}
		return conn, addr, nil
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	return conn, addr, err
}

// Dial opens an SSH connection to the sandbox host (directly or through a
// jumpbox resolved from ~/.ssh/config). Host keys are verified against the
// resolved known_hosts: an unknown host is a hard error, never silently trusted
// (no InsecureIgnoreHostKey).
func Dial(ctx context.Context, cfg DialConfig) (*SSHRunner, error) {
	plan, err := buildDialPlan(ctx, cfg)
	if err != nil {
		return nil, err
	}
	auth, err := authMethods(plan)
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := knownHostsCallback(plan.knownHosts)
	if err != nil {
		return nil, err
	}
	clientCfg := &ssh.ClientConfig{
		User:            plan.user,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
	}
	conn, addr, err := plan.dialTarget(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) {
			return nil, &HostKeyError{Host: plan.host, Port: plan.port, Mismatch: len(ke.Want) > 0, err: err}
		}
		return nil, fmt.Errorf("ssh handshake %s: %w%s", addr, err, proxyDetail(conn))
	}
	return &SSHRunner{client: ssh.NewClient(sshConn, chans, reqs)}, nil
}

// ResolveTarget reports the effective host/port/known_hosts for cfg — so the
// trust flow verifies and records the host key against the resolved HostName
// (the same key Dial uses), and works through a bastion.
func ResolveTarget(ctx context.Context, cfg DialConfig) (host string, port int, knownHosts string, err error) {
	p, err := buildDialPlan(ctx, cfg)
	if err != nil {
		return "", 0, "", err
	}
	return p.host, p.port, p.knownHosts, nil
}

// proxyDetail appends the bastion's stderr tail to an error when the connection
// went through a ProxyCommand (e.g. the jump host's "Permission denied").
func proxyDetail(conn net.Conn) string {
	if pc, ok := conn.(*proxyConn); ok {
		if s := pc.stderrString(); s != "" {
			return " (proxy: " + s + ")"
		}
	}
	return ""
}
