package shell

import "fmt"

// The shell iceclimber relays in is a fully-static bash (readline gives history +
// tab-completion with no extra bundle). We pin a specific upstream release and its
// per-arch SHA256 — the same trust model as the PBS/Node/Adoptium runtime sources,
// except the checksums are pinned here (the project publishes no SHASUMS file) and
// verified at download time. The build is musl-static, so ONE binary per arch runs on
// both musl and glibc boxes — libc is not part of the selection.
const (
	// bashVersion is the human version the pinned build reports (`bash --version`).
	bashVersion = "5.2.15"
	// bashRelease is the upstream release tag the assets are pinned to.
	bashRelease = "5.2.015-1.2.3-2"
)

// bashBaseURL is the pinned release's asset base; a var (not const) only so the installer
// test can point downloads at a local httptest server.
var bashBaseURL = "https://github.com/robxu9/bash-static/releases/download/" + bashRelease

// resolved is the exact bash asset to install for a sandbox arch.
type resolved struct {
	Version   string // e.g. "5.2.15"
	AssetName string // e.g. "bash-linux-aarch64"
	URL       string
	SHA256    string
}

// bashAsset is one pinned per-arch static bash, keyed by the probe arch.
type bashAsset struct {
	name   string
	sha256 string
}

// pinnedBash maps a sandbox arch to its pinned, checksum-verified static bash asset.
// SHA256s were computed from the pinned release's assets (verified to be statically
// linked ELF for the target arch); a mismatch at download aborts the install.
var pinnedBash = map[string]bashAsset{
	"x86_64":  {name: "bash-linux-x86_64", sha256: "64469a9512a00199c85622ec56f870f97d50457a4e06e0cfa39bae7adf0cc8f2"},
	"aarch64": {name: "bash-linux-aarch64", sha256: "8877ad33344af461ed801066322fd9a7808cd73e4e81087da228e32e8fad54ca"},
}

// resolve selects the pinned bash asset for the sandbox platform. Only linux + a known
// arch is supported; the musl-static build covers both libc families, so Libc is ignored.
func (i *Installer) resolve() (resolved, error) {
	if i.cfg.OS != "linux" {
		return resolved{}, fmt.Errorf("unsupported sandbox OS %q (only linux is supported)", i.cfg.OS)
	}
	a, ok := pinnedBash[i.cfg.Arch]
	if !ok {
		return resolved{}, fmt.Errorf("no pinned bash build for sandbox arch %q", i.cfg.Arch)
	}
	return resolved{
		Version:   bashVersion,
		AssetName: a.name,
		URL:       bashBaseURL + "/" + a.name,
		SHA256:    a.sha256,
	}, nil
}
