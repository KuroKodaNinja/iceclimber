package cli

import (
	"github.com/KuroKodaNinja/iceclimber/internal/audit"
	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/maven"
	"github.com/KuroKodaNinja/iceclimber/internal/node"
	"github.com/KuroKodaNinja/iceclimber/internal/npm"
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
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

// newNodeInstaller builds the Node installer from an open session.
func newNodeInstaller(sess *session) *node.Installer {
	return node.NewInstaller(node.Config{
		FS:       sess.fs,
		Runner:   sess.runner,
		Root:     sess.tree.Root,
		OS:       sess.fp.OS,
		Arch:     sess.fp.Arch,
		Libc:     sess.fp.Libc.Family,
		CacheDir: sess.cacheDir,
	})
}

// newJavaInstaller builds the JDK installer from an open session.
func newJavaInstaller(sess *session) *java.Installer {
	return java.NewInstaller(java.Config{
		FS:       sess.fs,
		Runner:   sess.runner,
		Root:     sess.tree.Root,
		OS:       sess.fp.OS,
		Arch:     sess.fp.Arch,
		Libc:     sess.fp.Libc.Family,
		CacheDir: sess.cacheDir,
	})
}

// mavenDeps builds the maven.install dependency bundle from an open session.
func mavenDeps(sess *session) maven.Deps {
	return maven.Deps{
		FS:        sess.fs,
		Runner:    sess.runner,
		Root:      sess.tree.Root,
		Arch:      sess.fp.Arch,
		Libc:      sess.fp.Libc.Family,
		MirrorURL: sess.maven.RepositoryURL,
		CacheDir:  sess.cacheDir,
	}
}

// pipDeps builds the pip.install dependency bundle from an open session.
func pipDeps(sess *session) pip.Deps {
	return pip.Deps{
		FS:                 sess.fs,
		Runner:             sess.runner,
		Root:               sess.tree.Root,
		Arch:               sess.fp.Arch,
		Libc:               sess.fp.Libc.Family,
		IndexURL:           sess.pip.IndexURL,
		ExtraIndexURL:      sess.pip.ExtraIndexURL,
		TrustedHost:        sess.pip.TrustedHost,
		ControllerPython:   sess.controllerPython,
		ControllerIndexURL: sess.pip.ControllerIndexURL,
	}
}

// npmDeps builds the npm.install dependency bundle from an open session.
func npmDeps(sess *session) npm.Deps {
	return npm.Deps{
		FS:                 sess.fs,
		Runner:             sess.runner,
		Root:               sess.tree.Root,
		Arch:               sess.fp.Arch,
		Libc:               sess.fp.Libc.Family,
		RegistryURL:        sess.npm.RegistryURL,
		ControllerNpm:      sess.controllerNpm,
		ControllerRegistry: sess.npm.ControllerRegistry,
	}
}

// webfetchDeps builds the web.fetch dependency bundle from an open session. The
// approver is non-nil only in interactive serve (inline egress approval).
func webfetchDeps(sess *session) webfetch.Deps {
	return webfetch.Deps{
		Runner:    sess.runner,
		FS:        sess.fs,
		Root:      sess.tree.Root,
		Policy:    sess.policy,
		Audit:     audit.New(sess.auditPath),
		SandboxID: sess.sandboxID,
		Approver:  sess.approver,
	}
}

// buildRegistry assembles the handler set Popo serves (the composition root —
// this is where heavier handlers get their dependencies, §9).
func buildRegistry(sess *session) protocol.Registry {
	return protocol.Registry{
		"ping":           protocol.PingHandler(version),
		"python.install": python.Handler(newInstaller(sess)),
		"pip.install":    pip.Handler(pipDeps(sess)),
		"node.install":   node.Handler(newNodeInstaller(sess)),
		"npm.install":    npm.Handler(npmDeps(sess)),
		"java.install":   java.Handler(newJavaInstaller(sess)),
		"maven.install":  maven.Handler(mavenDeps(sess)),
		"web.fetch":      webfetch.Handler(webfetchDeps(sess)),
	}
}
