package cli

import (
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
)

// newInstaller builds the Python installer from an open session.
func newInstaller(sess *session) *python.Installer {
	return python.NewInstaller(python.Config{
		FS:       sess.fs,
		Runner:   sess.runner,
		Root:     sess.tree.Root,
		OS:       sess.fp.OS,
		Arch:     sess.fp.Arch,
		Libc:     sess.fp.Libc.Family,
		CacheDir: sess.cacheDir,
	})
}

// pipDeps builds the pip.install dependency bundle from an open session.
func pipDeps(sess *session) pip.Deps {
	return pip.Deps{
		FS:            sess.fs,
		Runner:        sess.runner,
		Root:          sess.tree.Root,
		Arch:          sess.fp.Arch,
		Libc:          sess.fp.Libc.Family,
		IndexURL:      sess.pip.IndexURL,
		ExtraIndexURL: sess.pip.ExtraIndexURL,
		TrustedHost:   sess.pip.TrustedHost,
	}
}

// buildRegistry assembles the handler set Popo serves (the composition root —
// this is where heavier handlers get their dependencies, §9).
func buildRegistry(sess *session) protocol.Registry {
	return protocol.Registry{
		"ping":           protocol.PingHandler(version),
		"python.install": python.Handler(newInstaller(sess)),
		"pip.install":    pip.Handler(pipDeps(sess)),
	}
}
