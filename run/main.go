package main

import (
	"os"

	"github.com/paketo-buildpacks/packit"
	"github.com/paketo-buildpacks/packit/cargo"
	"github.com/paketo-buildpacks/packit/chronos"
	"github.com/paketo-buildpacks/packit/draft"
	"github.com/paketo-buildpacks/packit/postal"
	"github.com/paketo-buildpacks/packit/scribe"
	"github.com/paketo-buildpacks/yarn"
)

func main() {
	entryResolver := draft.NewPlanner()
	dependencyManager := postal.NewService(cargo.NewTransport())
	logEmitter := scribe.NewEmitter(os.Stdout)

	packit.Run(
		yarn.Detect(),
		yarn.Build(
			entryResolver,
			dependencyManager,
			chronos.DefaultClock,
			logEmitter,
		),
	)
}
