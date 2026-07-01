package cli

import (
	"github.com/KuroKodaNinja/iceclimber/internal/agent"
	"github.com/KuroKodaNinja/iceclimber/internal/audit"
	"github.com/KuroKodaNinja/iceclimber/internal/java"
	"github.com/KuroKodaNinja/iceclimber/internal/maven"
	"github.com/KuroKodaNinja/iceclimber/internal/node"
	"github.com/KuroKodaNinja/iceclimber/internal/npm"
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/progress"
	"github.com/KuroKodaNinja/iceclimber/internal/protocol"
	"github.com/KuroKodaNinja/iceclimber/internal/python"
	"github.com/KuroKodaNinja/iceclimber/internal/webfetch"
)

// newInstaller builds the Python installer from an open session. pr (may be nil)
// receives operator-facing progress events.
func newInstaller(sess *session, pr progress.Func) *python.Installer {
	return python.NewInstaller(python.Config{
		FS:       sess.fs,
		Runner:   sess.runner,
		Root:     sess.tree.Root,
		OS:       sess.fp.OS,
		Arch:     sess.fp.Arch,
		Libc:     sess.fp.Libc.Family,
		CacheDir: sess.cacheDir,
		Progress: pr,
	})
}

// newNodeInstaller builds the Node installer from an open session.
func newNodeInstaller(sess *session, pr progress.Func) *node.Installer {
	return node.NewInstaller(node.Config{
		FS:       sess.fs,
		Runner:   sess.runner,
		Root:     sess.tree.Root,
		OS:       sess.fp.OS,
		Arch:     sess.fp.Arch,
		Libc:     sess.fp.Libc.Family,
		CacheDir: sess.cacheDir,
		Progress: pr,
	})
}

// newAgentInstaller builds the agent installer (Claude Code, …) from an open
// session. The agent's native binary is relayed in from the controller, so it only
// needs the platform fingerprint, the cache, and the controller's npm registry.
func newAgentInstaller(sess *session) *agent.Installer {
	return agent.NewInstaller(agent.Config{
		FS:       sess.fs,
		Runner:   sess.runner,
		Root:     sess.tree.Root,
		OS:       sess.fp.OS,
		Arch:     sess.fp.Arch,
		Libc:     sess.fp.Libc.Family,
		CacheDir: sess.cacheDir,
		Registry: sess.npm.ControllerRegistry,
	})
}

// newJavaInstaller builds the JDK installer from an open session.
func newJavaInstaller(sess *session, pr progress.Func) *java.Installer {
	return java.NewInstaller(java.Config{
		FS:       sess.fs,
		Runner:   sess.runner,
		Root:     sess.tree.Root,
		OS:       sess.fp.OS,
		Arch:     sess.fp.Arch,
		Libc:     sess.fp.Libc.Family,
		CacheDir: sess.cacheDir,
		Progress: pr,
	})
}

// mavenDeps builds the maven.install dependency bundle from an open session.
func mavenDeps(sess *session, pr progress.Func) maven.Deps {
	return maven.Deps{
		FS:                   sess.fs,
		Runner:               sess.runner,
		Root:                 sess.tree.Root,
		Arch:                 sess.fp.Arch,
		Libc:                 sess.fp.Libc.Family,
		MirrorURL:            sess.maven.RepositoryURL,
		ControllerJava:       sess.controllerJava,
		ControllerRepository: sess.maven.ControllerRepository,
		CacheDir:             sess.cacheDir,
		Progress:             pr,
	}
}

// pipDeps builds the pip.install dependency bundle from an open session.
func pipDeps(sess *session, pr progress.Func) pip.Deps {
	src := sess.runtimeSourcesNow().Of("python")
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
		Progress:           pr,
		RuntimeMode:        string(src.Mode),
		SystemPath:         sess.systemRuntimePath("python", src),
		EnvManager:         src.EnvManager,
		CondaBin:           sess.condaPath(),
	}
}

// npmDeps builds the npm.install dependency bundle from an open session.
func npmDeps(sess *session, pr progress.Func) npm.Deps {
	return npm.Deps{
		FS:                 sess.fs,
		Runner:             sess.runner,
		Root:               sess.tree.Root,
		Arch:               sess.fp.Arch,
		Libc:               sess.fp.Libc.Family,
		RegistryURL:        sess.npm.RegistryURL,
		ControllerNpm:      sess.controllerNpm,
		ControllerRegistry: sess.npm.ControllerRegistry,
		Progress:           pr,
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
// this is where heavier handlers get their dependencies, §9). pr (may be nil) is the
// progress sink for agent-triggered installs: the console wires it so a Nana-initiated
// transfer lights up the in-flight indicator (#3); headless serve / the bootstrap smoke
// test pass nil (no render surface).
func buildRegistry(sess *session, pr progress.Func) protocol.Registry {
	return protocol.Registry{
		"ping":           protocol.PingHandler(version),
		"python.install": python.Handler(newInstaller(sess, pr)),
		"pip.install":    pip.Handler(pipDeps(sess, pr)),
		"node.install":   node.Handler(newNodeInstaller(sess, pr)),
		"npm.install":    npm.Handler(npmDeps(sess, pr)),
		"java.install":   java.Handler(newJavaInstaller(sess, pr)),
		"maven.install":  maven.Handler(mavenDeps(sess, pr)),
		"web.fetch":      webfetch.Handler(webfetchDeps(sess)),
	}
}
