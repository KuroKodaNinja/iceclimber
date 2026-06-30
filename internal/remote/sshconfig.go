package remote

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// errSSHUnavailable signals the OpenSSH client binary is absent, so config
// resolution can't run. Callers fall back to a direct dial from the literal
// DialConfig — corporate features (~/.ssh/config, ProxyJump) are simply skipped.
var errSSHUnavailable = errors.New("ssh client not found")

// resolvedSSH is the effective connection config OpenSSH computes for a host,
// captured from `ssh -G`. It is authoritative: it already applied Match/Include
// blocks and %-token expansion, so we never re-derive any of it ourselves.
type resolvedSSH struct {
	HostName        string
	Port            int
	User            string
	IdentityFiles   []string // ordered; may include nonexistent defaults (filtered at auth time)
	ProxyJump       string   // "" when none
	ProxyCommand    string   // "" when none/empty
	KnownHostsFiles []string // UserKnownHostsFile, space-split; "none" dropped
}

// resolveInput is the operator's explicit overrides, fed *into* `ssh -G` so
// OpenSSH does the token expansion (a resolved ProxyCommand's %h/%p/%r are then
// already correct for these values).
type resolveInput struct {
	Alias        string // the host (an alias or a literal host/IP)
	Port         int    // 0 = unset (let ssh_config / default decide)
	User         string // "" = unset
	IdentityFile string // "" = unset
	KnownHosts   string // "" = unset
	ConfigFile   string // "" = default ~/.ssh/config; non-empty → ssh -F
}

// args renders the `ssh -G …` argument vector for these inputs.
func (in resolveInput) args() []string {
	args := []string{"-G"}
	if in.ConfigFile != "" {
		args = append(args, "-F", in.ConfigFile)
	}
	if in.Port > 0 {
		args = append(args, "-p", strconv.Itoa(in.Port))
	}
	if in.User != "" {
		args = append(args, "-l", in.User)
	}
	if in.IdentityFile != "" {
		args = append(args, "-o", "IdentityFile="+in.IdentityFile)
	}
	if in.KnownHosts != "" {
		args = append(args, "-o", "UserKnownHostsFile="+in.KnownHosts)
	}
	return append(args, in.Alias)
}

// sshBinary locates the OpenSSH client, returning errSSHUnavailable if absent.
func sshBinary() (string, error) {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return "", errSSHUnavailable
	}
	return bin, nil
}

// runSSHG executes `ssh -G …` and returns stdout. It is a package var so tests
// can substitute fixture output without a real ssh client. `-G` never prompts or
// connects, so stderr is noise and discarded.
var runSSHG = func(ctx context.Context, sshBin string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, sshBin, args...)
	cmd.Stderr = io.Discard
	return cmd.Output()
}

// resolveSSHConfig asks OpenSSH for the effective config of in.Alias. Returns
// errSSHUnavailable when no ssh client is present.
func resolveSSHConfig(ctx context.Context, in resolveInput) (*resolvedSSH, error) {
	bin, err := sshBinary()
	if err != nil {
		return nil, err
	}
	out, err := runSSHG(ctx, bin, in.args())
	if err != nil {
		return nil, err
	}
	return parseSSHGOutput(bytes.NewReader(out)), nil
}

// parseSSHGOutput parses `ssh -G` output (lowercased "key value" lines, one per
// line). It is pure for unit testing. `identityfile` repeats are accumulated;
// `userknownhostsfile` is space-separated; literal "none"/empty proxy/known-hosts
// values are treated as absent.
func parseSSHGOutput(r io.Reader) *resolvedSSH {
	res := &resolvedSSH{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(key) {
		case "hostname":
			res.HostName = val
		case "port":
			if p, err := strconv.Atoi(val); err == nil {
				res.Port = p
			}
		case "user":
			res.User = val
		case "identityfile":
			if val != "" {
				res.IdentityFiles = append(res.IdentityFiles, val)
			}
		case "proxyjump":
			if !isNone(val) {
				res.ProxyJump = val
			}
		case "proxycommand":
			if !isNone(val) {
				res.ProxyCommand = val
			}
		case "userknownhostsfile":
			for _, f := range strings.Fields(val) {
				if !isNone(f) {
					res.KnownHostsFiles = append(res.KnownHostsFiles, f)
				}
			}
		}
	}
	return res
}

func isNone(s string) bool { return s == "" || strings.EqualFold(s, "none") }

// proxyArgv returns the argv for the ProxyCommand subprocess that yields a stdio
// byte-stream to the target, or nil for a direct dial. ProxyJump takes precedence
// over ProxyCommand (OpenSSH semantics). For ProxyJump j1,…,jn we synthesize
// `ssh [-F cfg] [-J j1,…,j(n-1)] -W HostName:Port jn` — the faithful equivalent of
// OpenSSH's own ProxyJump→ProxyCommand expansion, so the whole (multi-hop) chain
// and any bastion auth/2FA are handled by the ssh client. configFile (when set) is
// propagated as -F so jump-host aliases resolve against the same config we used.
func (r *resolvedSSH) proxyArgv(sshBin, configFile string) []string {
	if r.ProxyJump != "" {
		hops := strings.Split(r.ProxyJump, ",")
		last := strings.TrimSpace(hops[len(hops)-1])
		dest := r.HostName + ":" + strconv.Itoa(r.Port)
		argv := []string{sshBin}
		if configFile != "" {
			argv = append(argv, "-F", configFile)
		}
		if len(hops) > 1 {
			argv = append(argv, "-J", strings.Join(hops[:len(hops)-1], ","))
		}
		return append(argv, "-W", dest, last)
	}
	if r.ProxyCommand != "" {
		return []string{"/bin/sh", "-c", r.ProxyCommand}
	}
	return nil
}
