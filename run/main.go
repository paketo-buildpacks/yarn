package main

import (
	"github.com/paketo-buildpacks/packit"
	"github.com/paketo-buildpacks/yarn"
)

func main() {
	packit.Run(yarn.Detect(), yarn.Build())
}
