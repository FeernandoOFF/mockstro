package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/feernandooff/mockstro/internal/engine"
	"github.com/feernandooff/mockstro/internal/project"
	"github.com/feernandooff/mockstro/internal/web"
)

func uiCommand(g *globals, f *runFlags) *cobra.Command {
	var port int
	var noOpen bool
	c := &cobra.Command{
		Use:   "ui",
		Short: "Open the browser UI for starting and stopping mocks",
		Long: "Serves a local web page (127.0.0.1 only) that lists a project's scenarios, customers and flows and starts the mock on port 18080, like the TUI's Mock tab.\n\n" +
			"It opens the project containing the current directory (or --project). Anywhere else it reopens the last project it used, or lets you choose a folder in the page. " +
			"If a UI is already running on the port, this just opens it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return uiCmd(cmd.Context(), g, f, port, !noOpen) },
	}
	c.Flags().IntVar(&port, "port", web.DefaultPort, "port for the UI page")
	c.Flags().BoolVar(&noOpen, "no-open", false, "do not open the browser")
	return c
}

func uiCmd(ctx context.Context, g *globals, f *runFlags, port int, open bool) error {
	load := func(dir string) (*project.Project, engine.Options, error) {
		g2, f2 := *g, *f
		g2.projectDir = dir
		f2.serverPort = project.ReservedMockPort
		var p *project.Project
		var err error
		// The UI only serves mocks, so a suite without mockstro.yaml opens as lite.
		if root, findErr := project.Find(dir); findErr == nil && !project.HasConfig(root) {
			platform := g.platform
			if platform == "" {
				platform = project.GuessPlatform(root)
			}
			p, err = project.LoadLite(root, platform)
		} else {
			p, err = loadProject(&g2)
		}
		if err != nil {
			return nil, engine.Options{}, err
		}
		o, err := buildOptions("mock", &g2, &f2, p, nil)
		return p, o, err
	}

	initial := g.projectDir
	if initial == "" {
		if root, err := project.Find("."); err == nil {
			initial = root
		}
	}
	return web.Run(ctx, web.Config{Load: load, Initial: initial, Port: port, Open: open, Out: os.Stdout})
}
