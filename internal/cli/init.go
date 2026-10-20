package cli

import (
	"fmt"

	"github.com/feernandooff/mockstro/internal/project"
	"github.com/spf13/cobra"
)

func initCommand(g *globals) *cobra.Command {
	var appID, androidID, iosID, androidRoot, iosRoot, integration string
	cmd := &cobra.Command{Use: "init", Short: "Create a shared Maestro suite with per-platform app IDs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
			root, err := installRoot(g)
			if err != nil {
				return err
			}
			cfg := project.Config{Platforms: map[string]project.TargetConfig{}}
			if androidID != "" {
				cfg.Platforms["android"] = project.TargetConfig{AppID: androidID, Project: androidRoot, Integration: integration}
			}
			if iosID != "" {
				cfg.Platforms["ios"] = project.TargetConfig{AppID: iosID, Project: iosRoot, Integration: integration}
			}
			if err := project.Init(root, cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Created .maestro/mockstro.yaml and a shared ${APP_ID} smoke flow. Review build settings/devices and add UI assertions. Use bootstrap to add the pinned wrapper.")
			return nil
		}}
	cmd.Flags().StringVar(&androidID, "android-app-id", "", "Android application ID (enables Android)")
	cmd.Flags().StringVar(&appID, "app-id", "", "app ID for --platform (Android by default); use per-platform flags for a shared suite")
	cmd.Flags().StringVar(&iosID, "ios-app-id", "", "iOS bundle ID (enables iOS)")
	cmd.Flags().StringVar(&androidRoot, "android-project", ".", "Android source checkout, relative to the suite repository")
	cmd.Flags().StringVar(&iosRoot, "ios-project", ".", "iOS source checkout, relative to the suite repository")
	cmd.Flags().StringVar(&integration, "integration", "lite", "lite (installed app/proxy) or full (build and runtime override)")
	return cmd
}
