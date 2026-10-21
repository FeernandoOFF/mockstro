package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/feernandooff/mockstro/internal/bootstrap"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

func initCommand(g *globals, version string) *cobra.Command {
	var appID, androidID, iosID, androidRoot, iosRoot, integration, cliVersion string
	cmd := &cobra.Command{Use: "init", Short: "Set up this repo: create .maestro/mockstro.yaml if missing, pin the CLI version and add ./mockstrow", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			root, err := installRoot(g)
			if err != nil {
				return err
			}
			if appID != "" {
				if androidID != "" || iosID != "" {
					return fmt.Errorf("--app-id cannot be combined with --android-app-id or --ios-app-id")
				}
				switch g.platform {
				case "", "android":
					androidID = appID
				case "ios":
					iosID = appID
				default:
					return fmt.Errorf("unknown platform: %s", g.platform)
				}
			}
			if _, err := os.Stat(filepath.Join(root, ".maestro", "mockstro.yaml")); err == nil {
				if androidID != "" || iosID != "" {
					return fmt.Errorf(".maestro/mockstro.yaml already exists; edit it instead of passing app IDs")
				}
			} else if err := createSuite(cmd, root, androidID, iosID, androidRoot, iosRoot, integration); err != nil {
				return err
			}
			if cliVersion == "" && project.ValidateCLIVersion(version) == nil {
				cliVersion = version
			}
			switch _, err := os.Lstat(filepath.Join(root, "mockstrow")); {
			case err == nil:
				fmt.Fprintln(out, "Kept existing ./mockstrow.")
			case cliVersion == "":
				fmt.Fprintln(out, "Development build: pass --cli-version vX.Y.Z to add ./mockstrow.")
			default:
				if err := bootstrap.Project(root, cliVersion); err != nil {
					return err
				}
				fmt.Fprintf(out, "Added ./mockstrow and pinned cliVersion %s. Commit both, then run ./mockstrow check.\n", cliVersion)
			}
			return nil
		}}
	cmd.Flags().StringVar(&androidID, "android-app-id", "", "Android application ID (enables Android)")
	cmd.Flags().StringVar(&appID, "app-id", "", "app ID for --platform (Android by default); use per-platform flags for a shared suite")
	cmd.Flags().StringVar(&iosID, "ios-app-id", "", "iOS bundle ID (enables iOS)")
	cmd.Flags().StringVar(&androidRoot, "android-project", ".", "Android source checkout, relative to the suite repository")
	cmd.Flags().StringVar(&iosRoot, "ios-project", ".", "iOS source checkout, relative to the suite repository")
	cmd.Flags().StringVar(&integration, "integration", "lite", "lite (installed app/proxy) or full (build and runtime override)")
	cmd.Flags().StringVar(&cliVersion, "cli-version", "", "release tag to pin, e.g. v1.2.3 (defaults to this CLI's version)")
	return cmd
}

func createSuite(cmd *cobra.Command, root, androidID, iosID, androidRoot, iosRoot, integration string) error {
	out := cmd.OutOrStdout()
	if androidID == "" && iosID == "" && isTerminal(os.Stdin) {
		in := bufio.NewReader(os.Stdin)
		androidID = prompt(out, in, "Android app ID (blank to skip): ")
		iosID = prompt(out, in, "iOS bundle ID (blank to skip): ")
	}
	if androidID == "" && iosID == "" {
		return fmt.Errorf("provide an app ID, e.g. --android-app-id com.example.app --ios-app-id com.example.ios")
	}
	cfg := project.Config{Platforms: map[string]project.TargetConfig{}}
	if androidID != "" {
		cfg.Platforms["android"] = project.TargetConfig{AppID: androidID, Project: androidRoot, Integration: integration}
	}
	if iosID != "" {
		cfg.Platforms["ios"] = project.TargetConfig{AppID: iosID, Project: iosRoot, Integration: integration}
	}
	skipped, err := project.Init(root, cfg)
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		fmt.Fprintf(out, "Kept existing .maestro files: %s\n", strings.Join(skipped, ", "))
	}
	fmt.Fprintln(out, "Created .maestro/mockstro.yaml and a smoke flow. Review build settings and devices, and add UI assertions.")
	return nil
}

func isTerminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd())
}

func prompt(out io.Writer, in *bufio.Reader, label string) string {
	fmt.Fprint(out, label)
	line, _ := in.ReadString('\n')
	return strings.TrimSpace(line)
}
