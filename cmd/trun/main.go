package main

import (
	"os"

	"github.com/feernandooff/trun/internal/cli"
	"github.com/feernandooff/trun/internal/tui"
)

var version = "dev"

func main() {
	cli.TUIFunc = tui.Run
	os.Exit(cli.Execute(version))
}
