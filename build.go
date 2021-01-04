package yarn

import (
	"path/filepath"
	"time"

	"github.com/paketo-buildpacks/packit"
	"github.com/paketo-buildpacks/packit/chronos"
	"github.com/paketo-buildpacks/packit/postal"
)

//go:generate faux --interface EntryResolver --output fakes/entry_resolver.go
type EntryResolver interface {
	Resolve([]packit.BuildpackPlanEntry) packit.BuildpackPlanEntry
}

//go:generate faux --interface DependencyManager --output fakes/dependency_manager.go
type DependencyManager interface {
	Resolve(path, id, version, stack string) (postal.Dependency, error)
	Install(dependency postal.Dependency, cnbPath, layerPath string) error
}

//go:generate faux --interface BuildPlanRefinery --output fakes/build_plan_refinery.go
type BuildPlanRefinery interface {
	BillOfMaterials(postal.Dependency) packit.BuildpackPlanEntry
}

func Build(
	entries EntryResolver,
	dependencies DependencyManager,
	planRefinery BuildPlanRefinery,
	clock chronos.Clock,
	logger LogEmitter,
) packit.BuildFunc {
	return func(context packit.BuildContext) (packit.BuildResult, error) {
		logger.Title("%s %s", context.BuildpackInfo.Name, context.BuildpackInfo.Version)

		yarnLayer, err := context.Layers.Get(YarnLayerName)
		if err != nil {
			return packit.BuildResult{}, err
		}

		entry := entries.Resolve(context.Plan.Entries)
		entryVersion, _ := entry.Metadata["version"].(string)

		dependency, err := dependencies.Resolve(
			filepath.Join(context.CNBPath, "buildpack.toml"),
			entry.Name,
			entryVersion,
			context.Stack)
		if err != nil {
			return packit.BuildResult{}, err
		}

		bom := planRefinery.BillOfMaterials(dependency)

		cachedSHA, ok := yarnLayer.Metadata[DependencyCacheKey].(string)
		if ok && cachedSHA == dependency.SHA256 {
			logger.Process("Reusing cached layer %s", yarnLayer.Path)
			logger.Break()

			return packit.BuildResult{
				Plan: packit.BuildpackPlan{
					Entries: []packit.BuildpackPlanEntry{bom},
				},
				Layers: []packit.Layer{yarnLayer},
			}, nil
		}

		logger.Process("Executing build process")

		yarnLayer, err = yarnLayer.Reset()
		if err != nil {
			return packit.BuildResult{}, err
		}

		yarnLayer.Build = entry.Metadata["build"] == true
		yarnLayer.Cache = entry.Metadata["build"] == true
		yarnLayer.Launch = entry.Metadata["launch"] == true

		logger.Subprocess("Installing Yarn")

		duration, err := clock.Measure(func() error {
			return dependencies.Install(dependency, context.CNBPath, yarnLayer.Path)
		})
		if err != nil {
			return packit.BuildResult{}, err
		}

		logger.Action("Completed in %s", duration.Round(time.Millisecond))
		logger.Break()

		yarnLayer.Metadata = map[string]interface{}{
			DependencyCacheKey: dependency.SHA256,
			"built_at":         clock.Now().Format(time.RFC3339Nano),
		}

		return packit.BuildResult{
			Plan: packit.BuildpackPlan{
				Entries: []packit.BuildpackPlanEntry{bom},
			},
			Layers: []packit.Layer{yarnLayer},
		}, nil
	}
}
