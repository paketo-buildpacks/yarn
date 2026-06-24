package yarn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/paketo-buildpacks/packit/v2"
	"github.com/paketo-buildpacks/packit/v2/chronos"
	"github.com/paketo-buildpacks/packit/v2/draft"
	"github.com/paketo-buildpacks/packit/v2/postal"
	"github.com/paketo-buildpacks/packit/v2/sbom"
	"github.com/paketo-buildpacks/packit/v2/scribe"
)

//go:generate faux --interface DependencyManager --output fakes/dependency_manager.go
type DependencyManager interface {
	Resolve(path, id, version, stack string) (postal.Dependency, error)
	Deliver(dependency postal.Dependency, cnbPath, layerPath, platformPath string) error
	GenerateBillOfMaterials(dependencies ...postal.Dependency) []packit.BOMEntry
}

//go:generate faux --interface SBOMGenerator --output fakes/sbom_generator.go
type SBOMGenerator interface {
	GenerateFromDependency(dependency postal.Dependency, dir string) (sbom.SBOM, error)
}

func Build(
	dependencyManager DependencyManager,
	sbomGenerator SBOMGenerator,
	clock chronos.Clock,
	logger scribe.Emitter,
) packit.BuildFunc {
	return func(context packit.BuildContext) (packit.BuildResult, error) {
		logger.Title("%s %s", context.BuildpackInfo.Name, context.BuildpackInfo.Version)

		yarnLayer, err := context.Layers.Get(YarnLayerName)
		if err != nil {
			return packit.BuildResult{}, err
		}

		planner := draft.NewPlanner()
		entry, _ := planner.Resolve("yarn", context.Plan.Entries, nil)
		version, ok := entry.Metadata["version"].(string)
		if !ok {
			version = "default"
		}

		// Determine whether the app uses Yarn Berry via the packageManager field
		// in package.json (e.g. "yarn@4.x.x") or via BP_YARN_VERSION env var.
		dependencyID := YarnDependency
		if isBerry(context.WorkingDir, version) {
			dependencyID = BerryDependency
			// Reset version so the dependency constraint in buildpack.toml drives selection.
			version = "default"
		}

		dependency, err := dependencyManager.Resolve(
			filepath.Join(context.CNBPath, "buildpack.toml"),
			dependencyID,
			version,
			context.Stack)
		if err != nil {
			return packit.BuildResult{}, err
		}

		bom := dependencyManager.GenerateBillOfMaterials(dependency)

		launch, build := planner.MergeLayerTypes("yarn", context.Plan.Entries)

		var buildMetadata = packit.BuildMetadata{}
		var launchMetadata = packit.LaunchMetadata{}
		if build {
			buildMetadata = packit.BuildMetadata{BOM: bom}
		}

		if launch {
			launchMetadata = packit.LaunchMetadata{BOM: bom}
		}

		cachedSHA, ok := yarnLayer.Metadata[DependencyCacheKey].(string)
		if ok && postal.Checksum(dependency.Checksum).MatchString(cachedSHA) {
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

		// The @yarnpkg/cli-dist tarball ships bin/yarn as 0644; chmod it so it
		// is executable on PATH. bin/yarn.js already has the correct 0755 mode.
		if dependencyID == BerryDependency {
			yarnShim := filepath.Join(yarnLayer.Path, "bin", "yarn")
			if _, statErr := os.Stat(yarnShim); statErr == nil {
				if err := os.Chmod(yarnShim, 0755); err != nil {
					return packit.BuildResult{}, fmt.Errorf("failed to make berry yarn shim executable: %w", err)
				}
			}
		}

		logger.Action("Completed in %s", duration.Round(time.Millisecond))
		logger.Break()

		sbomDisabled, err := checkSbomDisabled()
		if err != nil {
			return packit.BuildResult{}, err
		}

		if sbomDisabled {
			logger.Subprocess("Skipping SBOM generation for Yarn")
			logger.Break()
		} else {
			logger.GeneratingSBOM(yarnLayer.Path)
			var sbomContent sbom.SBOM
			duration, err = clock.Measure(func() error {
				sbomContent, err = sbomGenerator.GenerateFromDependency(dependency, yarnLayer.Path)
				return err
			})
			if err != nil {
				return packit.BuildResult{}, err
			}

			logger.Action("Completed in %s", duration.Round(time.Millisecond))
			logger.Break()

			logger.FormattingSBOM(context.BuildpackInfo.SBOMFormats...)
			yarnLayer.SBOM, err = sbomContent.InFormats(context.BuildpackInfo.SBOMFormats...)
			if err != nil {
				return packit.BuildResult{}, err
			}
		}

		yarnLayer.Metadata = map[string]interface{}{
			DependencyCacheKey: dependency.Checksum,
			"dependency-id":    dependencyID,
		}

		return packit.BuildResult{
			Layers: []packit.Layer{yarnLayer},
			Build:  buildMetadata,
			Launch: launchMetadata,
		}, nil
	}
}

func checkSbomDisabled() (bool, error) {
	if disableStr, ok := os.LookupEnv("BP_DISABLE_SBOM"); ok {
		disable, err := strconv.ParseBool(disableStr)
		if err != nil {
			return false, fmt.Errorf("failed to parse BP_DISABLE_SBOM value %s: %w", disableStr, err)
		}
		return disable, nil
	}
	return false, nil
}

// isBerry returns true when the app declares a packageManager field in
// package.json that starts with "yarn@" and the major version is >= 2 (i.e.
// Yarn Berry), or when the resolved build-plan version string is >= "2".
func isBerry(workingDir, version string) bool {
	// Explicit version constraint wins first.
	if version != "" && version != "default" {
		major := strings.SplitN(version, ".", 2)[0]
		if major >= "2" {
			return true
		}
	}

	pm := readPackageManager(workingDir)
	if strings.HasPrefix(pm, "yarn@") {
		ver := strings.TrimPrefix(pm, "yarn@")
		major := strings.SplitN(ver, ".", 2)[0]
		if major >= "2" {
			return true
		}
	}
	return false
}

// readPackageManager reads the "packageManager" field from package.json in the
// given directory. Returns an empty string if the file cannot be read or the
// field is absent.
func readPackageManager(workingDir string) string {
	f, err := os.Open(filepath.Join(workingDir, "package.json"))
	if err != nil {
		return ""
	}
	defer f.Close()

	var pkg struct {
		PackageManager string `json:"packageManager"`
	}
	if err := json.NewDecoder(f).Decode(&pkg); err != nil {
		return ""
	}
	return pkg.PackageManager
}
