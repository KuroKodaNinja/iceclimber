package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/KuroKodaNinja/iceclimber/internal/conda"
	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/maven"
	"github.com/KuroKodaNinja/iceclimber/internal/npm"
	"github.com/KuroKodaNinja/iceclimber/internal/pip"
	"github.com/KuroKodaNinja/iceclimber/internal/pkg"
	"github.com/KuroKodaNinja/iceclimber/internal/runtimes"
	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	var runtimeSource string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install language runtimes or packages into the sandbox",
		Long: "Install a language runtime or packages. Choose where a runtime comes from —\n" +
			"iceclimber-managed (a pinned build) or the box's own (system) — with --runtime-source\n" +
			"here, or `runtimes.<lang>.source` in config; the console's install form offers the same\n" +
			"choice. (Runtime source is a post-bootstrap concern — bootstrap only sets up the tree.)",
		// Persist any --runtime-source choice before the subcommand resolves the runtime.
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return persistRuntimeSources(runtimeSource)
		},
	}
	cmd.PersistentFlags().StringVar(&runtimeSource, "runtime-source", "",
		"set + persist a per-language runtime source, e.g. python=system,node=managed (or python=system:conda)")
	cmd.AddCommand(newInstallPythonCmd(), newInstallPipCmd(), newInstallNodeCmd(), newInstallNpmCmd(),
		newInstallJavaCmd(), newInstallMavenCmd(), newInstallCondaCmd())
	return cmd
}

// persistRuntimeSources resolves the per-sandbox runtime-source store and merges a
// `--runtime-source` value onto it (empty is a no-op). The store overlay is factored into
// overlayRuntimeSources so it's unit-testable without loading config.
func persistRuntimeSources(flagStr string) error {
	if strings.TrimSpace(flagStr) == "" {
		return nil
	}
	cfg, err := config.Load(cfgFile, sandboxID)
	if err != nil {
		return err
	}
	return overlayRuntimeSources(runtimesStore(cfg), flagStr)
}

// overlayRuntimeSources parses a `--runtime-source` value (lang=mode[:env_manager], …) and
// merges it onto the persisted store, leaving unnamed languages untouched, so subsequent
// installs + serve honor it. Empty is a no-op. This is the CLI parity for the console install
// form's managed-vs-system choice — both overlay the same store (config stays the declarative
// path), mirroring consoleOps.SetRuntimeSources.
func overlayRuntimeSources(store *runtimes.Store, flagStr string) error {
	if strings.TrimSpace(flagStr) == "" {
		return nil
	}
	flagSrc, err := runtimes.ParseFlag(flagStr)
	if err != nil {
		return err
	}
	persisted, err := store.Load()
	if err != nil {
		return err
	}
	for lang, src := range flagSrc {
		persisted[lang] = src
	}
	return store.Save(persisted)
}

func newInstallMavenCmd() *cobra.Command {
	var transport, javaVersion, tier, mirror string
	cmd := &cobra.Command{
		Use:   "maven <group:artifact:version>...",
		Short: "Resolve JVM dependencies (Maven coordinates) into a classpath via Coursier",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			specs, err := parseCoords(args)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			deps := mavenDeps(sess, pr)
			if mirror != "" {
				deps.MirrorURL = mirror
			}
			res, err := maven.Run(ctx, deps, javaVersion, specs, tier)
			finish()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, p := range res.Installed {
				fmt.Fprintf(w, "resolved %s:%s (%s)\n", p.Name, p.Version, p.Tier)
			}
			for _, f := range res.Failed {
				fmt.Fprintf(w, "FAILED   %s:%s: %s\n", f.Name, f.Version, f.Error)
			}
			if res.Classpath != "" {
				fmt.Fprintf(w, "classpath: %s\n", res.Classpath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&javaVersion, "java", "", "target JDK feature version, e.g. 21 (required)")
	cmd.Flags().StringVar(&tier, "tier", "auto", "resolution tier: auto|mirror|relay")
	cmd.Flags().StringVar(&mirror, "mirror", "", "Maven repository URL (overrides config; Tier 0)")
	_ = cmd.MarkFlagRequired("java")
	return cmd
}

// parseCoords turns "group:artifact:version" args into specs (Name = "group:artifact").
func parseCoords(args []string) ([]pkg.Spec, error) {
	specs := make([]pkg.Spec, 0, len(args))
	for _, a := range args {
		parts := strings.SplitN(a, ":", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("invalid coordinate %q (want group:artifact:version)", a)
		}
		specs = append(specs, pkg.Spec{Name: parts[0] + ":" + parts[1], Version: parts[2]})
	}
	return specs, nil
}

func newInstallJavaCmd() *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "java <version>",
		Short: "Install a portable Temurin JDK (javac bundled)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			res, err := newJavaInstaller(sess, pr).Install(ctx, args[0])
			finish()
			if err != nil {
				return err
			}
			// In proxy mode, build the JVM truststore now so any JVM tool trusts the egress CA.
			if err := ensureEgressJavaTrust(ctx, sess, res.Path); err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			verb := "installed"
			if res.AlreadyInstalled {
				verb = "already installed:"
			}
			fmt.Fprintf(w, "%s java %s at %s\n", verb, res.Version, res.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	return cmd
}

func newInstallNpmCmd() *cobra.Command {
	var transport, nodeVersion, tier, project string
	cmd := &cobra.Command{
		Use:   "npm [<pkg>[@version]...]",
		Short: "Install npm packages, or a whole project's package.json (--project), into a Node runtime (Tier 0 mirror / Tier 1 relay)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" && len(args) == 0 {
				return fmt.Errorf("give package names or --project <dir> (a sandbox dir with package.json)")
			}
			if project != "" && len(args) > 0 {
				return fmt.Errorf("--project installs a package.json; don't also pass package names")
			}
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			var out npm.Result
			if project != "" {
				out, err = npm.RunProject(ctx, npmDeps(sess, pr), nodeVersion, project, tier)
			} else {
				out, err = npm.Run(ctx, npmDeps(sess, pr), nodeVersion, parseNpmSpecs(args), tier)
			}
			finish()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, p := range out.Installed {
				fmt.Fprintf(w, "installed %s %s (%s)\n", p.Name, p.Version, p.Tier)
			}
			for _, f := range out.Failed {
				fmt.Fprintf(w, "FAILED   %s %s: %s\n", f.Name, f.Version, f.Error)
			}
			fmt.Fprintf(w, "%d installed, %d failed\n", len(out.Installed), len(out.Failed))
			fmt.Fprintf(w, "the agent should export NODE_PATH=%s\n", out.NodePath)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&nodeVersion, "node", "", "target node version line, e.g. 20 (required)")
	cmd.Flags().StringVar(&tier, "tier", "auto", "resolution tier: auto|mirror|relay")
	cmd.Flags().StringVar(&project, "project", "", "sandbox project dir with a package.json — install its deps (npm install/ci) instead of named packages")
	_ = cmd.MarkFlagRequired("node")
	return cmd
}

// parseNpmSpecs turns "name" / "name@version" / "@scope/name@version" args into
// package specs (the leading @ of a scoped name is not a version separator).
func parseNpmSpecs(args []string) []pkg.Spec {
	specs := make([]pkg.Spec, 0, len(args))
	for _, a := range args {
		name, version := a, ""
		if i := strings.LastIndex(a, "@"); i > 0 {
			name, version = a[:i], a[i+1:]
		}
		specs = append(specs, pkg.Spec{Name: name, Version: version})
	}
	return specs
}

func newInstallNodeCmd() *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "node <version>",
		Short: "Install a portable Node.js runtime (npm bundled)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			res, err := newNodeInstaller(sess, pr).Install(ctx, args[0])
			finish()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			verb := "installed"
			if res.AlreadyInstalled {
				verb = "already installed:"
			}
			fmt.Fprintf(w, "%s node %s at %s\n", verb, res.Version, res.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	return cmd
}

func newInstallPythonCmd() *cobra.Command {
	var transport string
	cmd := &cobra.Command{
		Use:   "python <minor-version>",
		Short: "Install a portable Python runtime (python-build-standalone)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			res, err := newInstaller(sess, pr).Install(ctx, args[0])
			finish()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			verb := "installed"
			if res.AlreadyInstalled {
				verb = "already installed:"
			}
			fmt.Fprintf(w, "%s python %s at %s\n", verb, res.Version, res.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	return cmd
}

func newInstallPipCmd() *cobra.Command {
	var transport, pyVersion, tier string
	var pipArgs []string
	cmd := &cobra.Command{
		Use:   "pip <pkg>[==version]...",
		Short: "Install pip packages into an installed runtime (Tier 0 mirror / Tier 1 relay)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			out, err := pip.Run(ctx, pipDeps(sess, pr), pyVersion, parseSpecs(args), tier, pipArgs)
			finish()
			if err != nil {
				return err
			}
			printOutcome(cmd, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&pyVersion, "python", "", "target python minor version, e.g. 3.12 (required)")
	cmd.Flags().StringVar(&tier, "tier", "auto", "resolution tier: auto|mirror|relay")
	cmd.Flags().StringArrayVar(&pipArgs, "pip-arg", nil, "extra pip flag passed through (allowlisted), e.g. --pip-arg=--index-url --pip-arg=https://download.pytorch.org/whl/cpu (repeatable)")
	_ = cmd.MarkFlagRequired("python")
	return cmd
}

func newInstallCondaCmd() *cobra.Command {
	var transport, pyVersion, tier, file string
	var condaArgs []string
	cmd := &cobra.Command{
		Use:   "conda [<pkg>[==version]...]",
		Short: "Install conda packages, or a whole environment.yml (--file), into a conda env (Tier-0 channel / relay air-gapped)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" && (pyVersion == "" || len(args) == 0) {
				return fmt.Errorf("give --python and package names, or --file <dir>/environment.yml (a sandbox environment.yml)")
			}
			if file != "" && (len(args) > 0 || pyVersion != "" || len(condaArgs) > 0) {
				return fmt.Errorf("--file builds a whole environment.yml (its own channels); don't also pass --python, --conda-arg, or package names")
			}
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()
			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()
			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			var out pkg.Outcome
			if file != "" {
				out, err = conda.RunManifest(ctx, condaDeps(sess, pr), file, tier)
			} else {
				out, err = conda.Run(ctx, condaDeps(sess, pr), pyVersion, parseSpecs(args), tier, condaArgs)
			}
			finish()
			if err != nil {
				return err
			}
			printOutcome(cmd, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&pyVersion, "python", "", "target python minor version, e.g. 3.12 (required unless --file)")
	cmd.Flags().StringVar(&tier, "tier", "auto", "resolution tier: auto|mirror|relay")
	cmd.Flags().StringVar(&file, "file", "", "sandbox path to an environment.yml — build the whole env from it instead of named packages")
	cmd.Flags().StringArrayVar(&condaArgs, "conda-arg", nil, "extra conda flag passed through (allowlisted), e.g. --conda-arg=-c --conda-arg=conda-forge (repeatable)")
	return cmd
}

// parseSpecs turns "name" / "name==version" args into package specs.
func parseSpecs(args []string) []pkg.Spec {
	specs := make([]pkg.Spec, 0, len(args))
	for _, a := range args {
		name, version, _ := strings.Cut(a, "==")
		specs = append(specs, pkg.Spec{Name: name, Version: version})
	}
	return specs
}

func printOutcome(cmd *cobra.Command, out pkg.Outcome) {
	w := cmd.OutOrStdout()
	for _, p := range out.Installed {
		fmt.Fprintf(w, "installed %s %s (%s)\n", p.Name, p.Version, p.Tier)
	}
	for _, f := range out.Failed {
		fmt.Fprintf(w, "FAILED   %s %s: %s\n", f.Name, f.Version, f.Error)
	}
	fmt.Fprintf(w, "%d installed, %d failed\n", len(out.Installed), len(out.Failed))
}
