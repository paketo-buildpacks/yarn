package yarn

import (
	"path/filepath"
	"time"

	"github.com/paketo-buildpacks/packit"
	"github.com/paketo-buildpacks/packit/chronos"
	"github.com/paketo-buildpacks/packit/postal"
	"github.com/paketo-buildpacks/packit/sbom"
	"github.com/paketo-buildpacks/packit/scribe"
)

//go:generate faux --interface EntryResolver --output fakes/entry_resolver.go
type EntryResolver interface {
	Resolve(name string, entries []packit.BuildpackPlanEntry, priorites []interface{}) (packit.BuildpackPlanEntry, []packit.BuildpackPlanEntry)
	MergeLayerTypes(name string, entries []packit.BuildpackPlanEntry) (launch, build bool)
}

//go:generate faux --interface DependencyManager --output fakes/dependency_manager.go
type DependencyManager interface {
	Resolve(path, id, version, stack string) (postal.Dependency, error)
	Deliver(dependency postal.Dependency, cnbPath, layerPath, platformPath string) error
}

func Build(
	entryResolver EntryResolver,
	dependencyManager DependencyManager,
	clock chronos.Clock,
	logger scribe.Emitter,
) packit.BuildFunc {
	return func(context packit.BuildContext) (packit.BuildResult, error) {
		logger.Title("%s %s", context.BuildpackInfo.Name, context.BuildpackInfo.Version)

		yarnLayer, err := context.Layers.Get(YarnLayerName)
		if err != nil {
			return packit.BuildResult{}, err
		}

		entry, _ := entryResolver.Resolve("yarn", context.Plan.Entries, nil)
		version, ok := entry.Metadata["version"].(string)
		if !ok {
			version = "default"
		}

		dependency, err := dependencyManager.Resolve(
			filepath.Join(context.CNBPath, "buildpack.toml"),
			entry.Name,
			version,
			context.Stack)
		if err != nil {
			return packit.BuildResult{}, err
		}

		launch, build := entryResolver.MergeLayerTypes("yarn", context.Plan.Entries)

		bom, err := sbom.GenerateFromDependency(dependency, yarnLayer.Path)
		if err != nil {
			panic(err)
		}

		var buildMetadata = packit.BuildMetadata{}
		var launchMetadata = packit.LaunchMetadata{}
		bomEntries := make(packit.SBOMEntries)
		bomEntries.Set("cdx.json", bom.Format(sbom.CycloneDXFormat))
		bomEntries.Set("syft.json", bom.Format(sbom.SyftFormat))
		bomEntries.Set("spdx.json", bom.Format(sbom.SPDXFormat))

		if build {
			buildMetadata.SBOM = bomEntries
		}

		if launch {
			launchMetadata.SBOM = bomEntries
		}

		yarnLayer.Launch, yarnLayer.Build, yarnLayer.Cache = launch, build, build

		yarnLayer.SBOM.Set("cdx.json", bom.Format(sbom.CycloneDXFormat))
		yarnLayer.SBOM.Set("syft.json", bom.Format(sbom.SyftFormat))
		yarnLayer.SBOM.Set("spdx.json", bom.Format(sbom.SPDXFormat))

		cachedSHA, ok := yarnLayer.Metadata[DependencyCacheKey].(string)
		if ok && cachedSHA == dependency.SHA256 {
			logger.Process("Reusing cached layer %s", yarnLayer.Path)
			logger.Break()

			yarnLayer.Launch, yarnLayer.Build, yarnLayer.Cache = launch, build, build

			return packit.BuildResult{
				Layers: []packit.Layer{yarnLayer},
				Build:  buildMetadata,
				Launch: launchMetadata,
			}, nil
		}

		logger.Process("Executing build process")

		yarnLayer, err = yarnLayer.Reset()
		if err != nil {
			return packit.BuildResult{}, err
		}

		yarnLayer.Launch, yarnLayer.Build, yarnLayer.Cache = launch, build, build

		logger.Subprocess("Installing Yarn")

		duration, err := clock.Measure(func() error {
			return dependencyManager.Deliver(dependency, context.CNBPath, yarnLayer.Path, context.Platform.Path)
		})
		if err != nil {
			return packit.BuildResult{}, err
		}

		logger.Action("Completed in %s", duration.Round(time.Millisecond))
		logger.Break()

		yarnLayer.SBOM.Set("cdx.json", bom.Format(sbom.CycloneDXFormat))
		yarnLayer.SBOM.Set("syft.json", bom.Format(sbom.SyftFormat))
		yarnLayer.SBOM.Set("spdx.json", bom.Format(sbom.SPDXFormat))

		yarnLayer.Metadata = map[string]interface{}{
			DependencyCacheKey: dependency.SHA256,
			"built_at":         clock.Now().Format(time.RFC3339Nano),
		}

		return packit.BuildResult{
			Layers: []packit.Layer{yarnLayer},
			Build:  buildMetadata,
			Launch: launchMetadata,
		}, nil
	}
}
