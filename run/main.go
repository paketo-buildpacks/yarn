package main

import (
	"os"

	"github.com/paketo-buildpacks/packit"
	"github.com/paketo-buildpacks/packit/cargo"
	"github.com/paketo-buildpacks/packit/chronos"
	"github.com/paketo-buildpacks/packit/postal"
	"github.com/paketo-buildpacks/yarn"
)

func main() {
	entryResolver := yarn.NewPlanEntryResolver()
	dependencyManager := postal.NewService(cargo.NewTransport())
	planRefinery := yarn.NewPlanRefinery()
	logEmitter := yarn.NewLogEmitter(os.Stdout)

	packit.Run(
		yarn.Detect(),
		yarn.Build(
			entryResolver,
			dependencyManager,
			planRefinery,
			chronos.DefaultClock,
			logEmitter,
		),
	)
}
