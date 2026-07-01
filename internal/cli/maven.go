package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/KuroKodaNinja/iceclimber/internal/config"
	"github.com/KuroKodaNinja/iceclimber/internal/maven"
)

// newMavenCmd groups Maven build-tool actions (distinct from `install maven`, which
// resolves coordinates to a classpath). Today: `maven build`.
func newMavenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maven",
		Short: "Run the Maven build tool in the sandbox (air-gapped)",
	}
	cmd.AddCommand(newMavenBuildCmd())
	return cmd
}

func newMavenBuildCmd() *cobra.Command {
	var transport, javaVersion, mavenVersion, project string
	cmd := &cobra.Command{
		Use:   "build [goal...]",
		Short: "Build a sandbox Maven project (pom.xml) with mvn, air-gapped: the controller primes an offline .m2 repo, the sandbox runs `mvn -o package`",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" || javaVersion == "" {
				return fmt.Errorf("--project <dir> (a sandbox dir with pom.xml) and --java <feature> are required")
			}
			cfg, err := config.Load(cfgFile, sandboxID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Minute)
			defer cancel()
			sess, err := openSession(ctx, cfg, transport)
			if err != nil {
				return err
			}
			defer sess.Close()

			pr, finish := installProgress(cmd.OutOrStdout(), sess.transport)
			res, err := maven.Build(ctx, mavenBuildDeps(sess, pr), project, javaVersion, mavenVersion, args)
			finish()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, a := range res.Artifacts {
				fmt.Fprintf(w, "built %s (%s)\n", a, res.Tier)
			}
			if len(res.Artifacts) == 0 {
				fmt.Fprintln(w, "build succeeded (no target/*.jar produced)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "auto", "remote FS transport: auto|sftp|exec")
	cmd.Flags().StringVar(&javaVersion, "java", "", "sandbox JDK feature version for JAVA_HOME, e.g. 21 (required)")
	cmd.Flags().StringVar(&project, "project", "", "sandbox project dir with a pom.xml (required)")
	cmd.Flags().StringVar(&mavenVersion, "maven-version", "", "Apache Maven version to relay (default: match the controller's mvn)")
	return cmd
}
