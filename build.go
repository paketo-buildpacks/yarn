package yarn

import (
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "time"
    "encoding/json"

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

        // Detect Yarn Berry (modern) by presence of .yarnrc.yml/.yarnrc.yaml
        isBerryYarn := false
        if _, statErr := os.Stat(filepath.Join(context.WorkingDir, ".yarnrc.yml")); statErr == nil {
            isBerryYarn = true
        } else if _, statErr := os.Stat(filepath.Join(context.WorkingDir, ".yarnrc.yaml")); statErr == nil {
            isBerryYarn = true
        }

        // If package.json declares packageManager: "yarn@<ver>", prefer that ONLY for Berry projects
        var pmYarnVersion string
        if data, readErr := os.ReadFile(filepath.Join(context.WorkingDir, "package.json")); readErr == nil {
            var pkg struct {
                PackageManager string `json:"packageManager"`
            }
            if jsonErr := json.Unmarshal(data, &pkg); jsonErr == nil {
                pm := strings.TrimSpace(pkg.PackageManager)
                if strings.HasPrefix(pm, "yarn@") {
                    v := strings.TrimPrefix(pm, "yarn@")
                    if idx := strings.IndexAny(v, " +#"); idx != -1 {
                        v = v[:idx]
                    }
                    pmYarnVersion = v
                }
            }
        }
		dependency, err := dependencyManager.Resolve(
			filepath.Join(context.CNBPath, "buildpack.toml"),
			entry.Name,
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

        // Use resolved dependency version as the install version (override with packageManager only for Berry)
		
        resolvedInstallVersion := dependency.Version
        if isBerryYarn && pmYarnVersion != "" {
            resolvedInstallVersion = pmYarnVersion
        }

        cachedSHA, ok := yarnLayer.Metadata[DependencyCacheKey].(string)
        if ok && !isBerryYarn && postal.Checksum(dependency.Checksum).MatchString(cachedSHA) {
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

        logger.Subprocess("Installing Yarn %s", resolvedInstallVersion)

        var duration time.Duration
        if isBerryYarn {
            // Persist Corepack cache under the yarn layer so the runtime doesn't re-download Yarn
            corepackDir := filepath.Join(yarnLayer.Path, "corepack")
            if mkErr := os.MkdirAll(corepackDir, 0o755); mkErr != nil {
                return packit.BuildResult{}, mkErr
            }
			

            duration, err = clock.Measure(func() error {
                steps := [][]string{
                    {"corepack", "enable"},
                    {"corepack", "prepare", fmt.Sprintf("yarn@%s", resolvedInstallVersion), "--activate"},
                }
                for _, args := range steps {

                    cmd := exec.Command(args[0], args[1:]...)
                    cmd.Env = append(os.Environ(), "COREPACK_HOME="+corepackDir)
                    cmd.Stdout = os.Stdout
                    cmd.Stderr = os.Stderr
                    if err := cmd.Run(); err != nil {
                        return err
                    }
                }
                return nil
            })
            if err != nil {
                return packit.BuildResult{}, err
            }
            logger.Action("Completed in %s", duration.Round(time.Millisecond))
            logger.Break()
            // Ensure COREPACK_HOME is present in build and launch images
            yarnLayer.Build = true
            yarnLayer.Launch = true
            yarnLayer.BuildEnv.Default("COREPACK_HOME", filepath.Join(yarnLayer.Path, "corepack"))
            yarnLayer.LaunchEnv.Default("COREPACK_HOME", filepath.Join(yarnLayer.Path, "corepack"))
        } else {
            duration, err = clock.Measure(func() error {
                return dependencyManager.Deliver(dependency, context.CNBPath, yarnLayer.Path, context.Platform.Path)
            })
            if err != nil {
                return packit.BuildResult{}, err
            }
            logger.Action("Completed in %s", duration.Round(time.Millisecond))
            logger.Break()
        }

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

        cacheValue := dependency.Checksum
        if isBerryYarn || cacheValue == "" {
            cacheValue = dependency.Version
        }
        yarnLayer.Metadata = map[string]interface{}{
            DependencyCacheKey: cacheValue,
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
