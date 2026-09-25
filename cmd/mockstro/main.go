package main

import (
	"os"

	"github.com/feernandooff/mockstro/internal/cli"
	"github.com/feernandooff/mockstro/internal/tui"
)

var version = "dev"

func main() {
	cli.TUIFunc = tui.Run
	os.Exit(cli.Execute(version))
}
