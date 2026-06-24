package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/ProtonMail/go-crypto/openpgp"
	buildpackConfig "github.com/paketo-buildpacks/libdependency/buildpack_config"
	"github.com/paketo-buildpacks/libdependency/retrieve"
	"github.com/paketo-buildpacks/libdependency/upstream"
	"github.com/paketo-buildpacks/libdependency/versionology"
	"github.com/paketo-buildpacks/packit/v2/cargo"

	"crypto/sha256"
	"io"
)

const (
	// berryDependencyID distinguishes Berry entries from Classic in buildpack.toml.
	berryDependencyID = "berry"
	berryTagPrefix    = "@yarnpkg/cli/"
)

type Asset struct {
	BrowserDownloadUrl string `json:"browser_download_url"`
}

type YarnMetadata struct {
	SemverVersion *semver.Version
}

func (yarnMetadata YarnMetadata) Version() *semver.Version {
	return yarnMetadata.SemverVersion
}

func main() {
	buildpackTomlPath, output := retrieve.FetchArgs()

	config, err := buildpackConfig.ParseBuildpackToml(buildpackTomlPath)
	if err != nil {
		panic(err)
	}

	// Default to linux/amd64 if no targets are specified (mirrors NewMetadataWithPlatforms).
	if len(config.Targets) == 0 {
		config.Targets = []cargo.ConfigTarget{{OS: "linux", Arch: "amd64"}}
	}

	var allDependencies []versionology.Dependency

	// --- Classic Yarn (id: "yarn") ---
	classicVersions, err := retrieve.GetNewVersionsForId("yarn", config, getAllVersions)
	if err != nil {
		panic(fmt.Errorf("could not get new Classic Yarn versions: %w", err))
	}
	for _, target := range config.Targets {
		platform := retrieve.Platform{OS: target.OS, Arch: target.Arch}
		allDependencies = append(allDependencies, retrieve.GenerateAllMetadataWithPlatform(classicVersions, generateMetadataWithPlatform, platform)...)
	}

	// --- Yarn Berry (id: "berry") ---
	berryVersions, err := retrieve.GetNewVersionsForId("berry", config, getAllBerryVersions)
	if err != nil {
		panic(fmt.Errorf("could not get new Berry versions: %w", err))
	}
	// Pre-fetch Berry releases once to avoid one API call per version.
	berryReleases, err := NewGithubClient(NewWebClient()).GetReleaseTags("yarnpkg", "berry")
	if err != nil {
		panic(fmt.Errorf("could not get Berry releases: %w", err))
	}
	for _, target := range config.Targets {
		platform := retrieve.Platform{OS: target.OS, Arch: target.Arch}
		allDependencies = append(allDependencies, retrieve.GenerateAllMetadataWithPlatform(berryVersions, func(vf versionology.VersionFetcher, p retrieve.Platform) ([]versionology.Dependency, error) {
			return generateBerryMetadataWithReleases(vf, berryReleases, p)
		}, platform)...)
	}

	// Write combined output.
	metadataJSON, err := json.Marshal(allDependencies)
	if err != nil {
		panic(fmt.Errorf("unable to marshal metadata JSON: %w", err))
	}
	if err = os.WriteFile(output, metadataJSON, os.ModePerm); err != nil {
		panic(fmt.Errorf("cannot write to %s: %w", output, err))
	}
	fmt.Printf("Wrote metadata to %s\n", output)
}

func generateMetadataWithPlatform(versionFetcher versionology.VersionFetcher, platform retrieve.Platform) ([]versionology.Dependency, error) {
	version := versionFetcher.Version().String()

	releases, err := NewGithubClient(NewWebClient()).GetReleaseTags("yarnpkg", "yarn")
	if err != nil {
		return nil, fmt.Errorf("could not get releases: %w", err)
	}

	for _, release := range releases {
		tagName := "v" + version
		if release.TagName == tagName {
			dependency, err := createDependencyVersion(version, tagName, platform)
			if err != nil {
				return nil, fmt.Errorf("could not create yarn version: %w", err)
			}

			return []versionology.Dependency{{
				ConfigMetadataDependency: dependency,
				SemverVersion:            versionFetcher.Version(),
			}}, nil
		}
	}

	return nil, fmt.Errorf("could not find yarn version %s", version)
}

func getAllVersions() (versionology.VersionFetcherArray, error) {
	githubClient := NewGithubClient(NewWebClient())
	releases, err := githubClient.GetReleaseTags("yarnpkg", "yarn")
	if err != nil {
		return nil, fmt.Errorf("could not get releases: %w", err)
	}

	var versions []versionology.VersionFetcher
	for _, release := range releases {
		versionTagName := strings.TrimPrefix(release.TagName, "v")
		version, err := semver.NewVersion(versionTagName)
		if err != nil {
			return nil, fmt.Errorf("failed to parse version: %w", err)
		}
		/** Versions less than 0.7.0 does not have source code and the version tag does not contains the "v" at the start*/
		if version.LessThan(semver.MustParse("0.7.0")) {
			continue
		}
		if version.Prerelease() != "" {
			continue
		}

		versions = append(versions, YarnMetadata{version})
	}

	return versions, nil
}

func createDependencyVersion(version, tagName string, platform retrieve.Platform) (cargo.ConfigMetadataDependency, error) {
	webClient := NewWebClient()
	githubClient := NewGithubClient(webClient)

	yarnGPGKey, err := webClient.Get("https://dl.yarnpkg.com/debian/pubkey.gpg")
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not get yarn GPG key: %w", err)
	}

	releaseAssetDir, err := os.MkdirTemp("", "yarn")
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(releaseAssetDir)
	releaseAssetPath := filepath.Join(releaseAssetDir, fmt.Sprintf("yarn-%s.tar.gz", tagName))

	assetName := fmt.Sprintf("yarn-%s.tar.gz", tagName)
	assetUrl, err := githubClient.DownloadReleaseAsset("yarnpkg", "yarn", tagName, assetName, releaseAssetPath)
	if err != nil {
		if errors.Is(err, AssetNotFound{AssetName: assetName}) {
			return cargo.ConfigMetadataDependency{}, NoSourceCodeError{Version: version}
		}
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not download asset url: %w", err)
	}

	assetContent, err := webClient.Get(assetUrl)
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not get asset content from asset url: %w", err)
	}

	asset := Asset{}
	err = json.Unmarshal(assetContent, &asset)
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not unmarshal asset url content: %w", err)
	}

	assetName = fmt.Sprintf("yarn-%s.tar.gz.asc", tagName)
	releaseAssetSignature, err := githubClient.GetReleaseAsset("yarnpkg", "yarn", tagName, assetName)
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not get release artifact signature: %w", err)
	}

	err = verifyASC(string(releaseAssetSignature), releaseAssetPath, string(yarnGPGKey))
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("release artifact signature verification failed: %w", err)
	}

	dependencySHA, err := getSHA256(releaseAssetPath)
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not get SHA256: %w", err)
	}

	return cargo.ConfigMetadataDependency{
		Arch:            platform.Arch,
		CPE:             fmt.Sprintf("cpe:2.3:a:yarnpkg:yarn:%s:*:*:*:*:*:*:*", version),
		Checksum:        fmt.Sprintf("sha256:%s", dependencySHA),
		DeprecationDate: nil,
		ID:              "yarn",
		Licenses:        retrieve.LookupLicenses(asset.BrowserDownloadUrl, upstream.DefaultDecompress),
		Name:            "Yarn",
		OS:              platform.OS,
		PURL:            retrieve.GeneratePURL("yarn", version, dependencySHA, asset.BrowserDownloadUrl),
		Source:          asset.BrowserDownloadUrl,
		SourceChecksum:  fmt.Sprintf("sha256:%s", dependencySHA),
		StripComponents: 1,
		Stacks:          []string{"io.buildpacks.stacks.bionic", "io.buildpacks.stacks.jammy", "*"},
		URI:             asset.BrowserDownloadUrl,
		Version:         version,
	}, nil
}

// getAllBerryVersions fetches all stable Yarn Berry versions from GitHub releases.
func getAllBerryVersions() (versionology.VersionFetcherArray, error) {
	githubClient := NewGithubClient(NewWebClient())
	releases, err := githubClient.GetReleaseTags("yarnpkg", "berry")
	if err != nil {
		return nil, fmt.Errorf("could not get Berry versions: %w", err)
	}

	var versions []versionology.VersionFetcher
	for _, release := range releases {
		versionStr := strings.TrimPrefix(release.TagName, berryTagPrefix)
		version, err := semver.NewVersion(versionStr)
		if err != nil {
			continue
		}
		if version.Prerelease() != "" {
			continue
		}
		versions = append(versions, YarnMetadata{version})
	}

	return versions, nil
}

// generateBerryMetadataWithReleases creates dependency metadata for a specific Berry version using pre-fetched releases.
func generateBerryMetadataWithReleases(versionFetcher versionology.VersionFetcher, releases []GithubRelease, platform retrieve.Platform) ([]versionology.Dependency, error) {
	version := versionFetcher.Version().String()

	for _, release := range releases {
		tagName := berryTagPrefix + version
		if release.TagName == tagName {
			dependency, err := createBerryDependencyVersion(version, tagName, platform)
			if err != nil {
				return nil, fmt.Errorf("could not create berry version: %w", err)
			}

			return []versionology.Dependency{{
				ConfigMetadataDependency: dependency,
				SemverVersion:            versionFetcher.Version(),
			}}, nil
		}
	}

	return nil, fmt.Errorf("could not find berry version %s", version)
}

// createBerryDependencyVersion builds a ConfigMetadataDependency for a Berry version.
// Downloads the @yarnpkg/cli-dist npm tarball which contains the ready-to-run
// bin/yarn.js bundle (strip-components=1 places bin/ into the layer).
func createBerryDependencyVersion(version, tagName string, platform retrieve.Platform) (cargo.ConfigMetadataDependency, error) {
	webClient := NewWebClient()

	downloadURL := fmt.Sprintf(
		"https://registry.npmjs.org/@yarnpkg/cli-dist/-/cli-dist-%s.tgz",
		version,
	)

	tempDir, err := os.MkdirTemp("", "berry")
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	tgzPath := filepath.Join(tempDir, fmt.Sprintf("cli-dist-%s.tgz", version))
	if err = webClient.Download(downloadURL, tgzPath); err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not download Berry cli-dist: %w", err)
	}

	dependencySHA, err := getSHA256(tgzPath)
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not compute SHA256: %w", err)
	}

	return cargo.ConfigMetadataDependency{
		Arch:            platform.Arch,
		CPE:             fmt.Sprintf("cpe:2.3:a:yarnpkg:yarn:%s:*:*:*:*:*:*:*", version),
		Checksum:        fmt.Sprintf("sha256:%s", dependencySHA),
		DeprecationDate: nil,
		ID:              berryDependencyID,
		Licenses:        retrieve.LookupLicenses(downloadURL, upstream.DefaultDecompress),
		Name:            "Yarn Berry",
		OS:              platform.OS,
		PURL:            retrieve.GeneratePURL("berry", version, dependencySHA, downloadURL),
		Source:          downloadURL,
		SourceChecksum:  fmt.Sprintf("sha256:%s", dependencySHA),
		StripComponents: 1,
		Stacks:          []string{"io.buildpacks.stacks.bionic", "io.buildpacks.stacks.jammy", "*"},
		URI:             downloadURL,
		Version:         version,
	}, nil
}

func verifyASC(asc, path string, pgpKeys ...string) error {
	if len(pgpKeys) == 0 {
		return errors.New("no pgp keys provided")
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("could not open file: %w", err)
	}
	defer file.Close()

	for _, pgpKey := range pgpKeys {
		keyring, err := openpgp.ReadArmoredKeyRing(strings.NewReader(pgpKey))
		if err != nil {
			log.Printf("could not read armored key ring: %s", err.Error())
			continue
		}

		_, err = openpgp.CheckArmoredDetachedSignature(keyring, file, strings.NewReader(asc), nil)
		if err != nil {
			log.Printf("failed to check signature: %s", err.Error())
			continue
		}
		log.Printf("found valid pgp key")
		return nil
	}

	return errors.New("no valid pgp keys provided")
}

func getSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "nil", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	_, err = io.Copy(hash, file)
	if err != nil {
		return "nil", fmt.Errorf("failed to calculate SHA256: %w", err)
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
