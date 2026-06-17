package main

import (
	"log"
	"os"

	"github.com/rancher/rke2-patcher/internal/cmd"
)

func main() {
	log.SetFlags(0)

	app := cmd.BuildCLIApp()
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
