package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/paketo-buildpacks/packit/v2/fs"
)

const (
	yarnDependencyID  = "yarn"
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
	validate(buildpackTomlPath, output)

	config, err := buildpackConfig.ParseBuildpackToml(buildpackTomlPath)
	if err != nil {
		panic(err)
	}

	// Default to linux/amd64 if no targets are specified (mirrors NewMetadataWithPlatforms).
	if len(config.Targets) == 0 {
		config.Targets = []cargo.ConfigTarget{{OS: "linux", Arch: "amd64"}}
	}

	var allDependencies []versionology.Dependency

	for _, job := range []struct {
		id       string
		versions retrieve.GetAllVersionsFunc
		meta     retrieve.GenerateMetadataWithPlatformFunc
	}{
		{yarnDependencyID, getClassicVersions, generateClassicMetadata},
		{berryDependencyID, getBerryVersions, generateBerryMetadata},
	} {
		newVersions, err := retrieve.GetNewVersionsForId(job.id, config, job.versions)
		if err != nil {
			panic(fmt.Errorf("could not get new versions for %s: %w", job.id, err))
		}
		for _, target := range config.Targets {
			platform := retrieve.Platform{OS: target.OS, Arch: target.Arch}
			allDependencies = append(allDependencies, retrieve.GenerateAllMetadataWithPlatform(newVersions, job.meta, platform)...)
		}
	}

	metadataJSON, err := json.Marshal(allDependencies)
	if err != nil {
		panic(fmt.Errorf("unable to marshal metadata JSON: %w", err))
	}
	if err = os.WriteFile(output, metadataJSON, os.ModePerm); err != nil {
		panic(fmt.Errorf("cannot write to %s: %w", output, err))
	}
	fmt.Printf("Wrote metadata to %s\n", output)
}

// validate function, is an exact copy of livedependency/retrieve/validate function
func validate(buildpackTomlPath, metadataFile string) {
	if exists, err := fs.Exists(buildpackTomlPath); err != nil {
		panic(err)
	} else if !exists {
		panic(fmt.Errorf("could not locate buildpack.toml at '%s'", buildpackTomlPath))
	}

	if metadataFile == "" {
		panic("metadataFile is required")
	}
}

func generateClassicMetadata(versionFetcher versionology.VersionFetcher, platform retrieve.Platform) ([]versionology.Dependency, error) {
	version := versionFetcher.Version().String()
	tagName := "v" + version

	releases, err := NewGithubClient(NewWebClient()).GetReleaseTags("yarnpkg", "yarn")
	if err != nil {
		return nil, fmt.Errorf("could not get releases: %w", err)
	}

	for _, release := range releases {
		if release.TagName != tagName {
			continue
		}

		dependency, err := createDependencyVersion(version, tagName, platform)
		if err != nil {
			return nil, fmt.Errorf("could not create yarn version: %w", err)
		}

		return []versionology.Dependency{{
			ConfigMetadataDependency: dependency,
			SemverVersion:            versionFetcher.Version(),
		}}, nil
	}

	return nil, fmt.Errorf("could not find yarn version %s", version)
}

func generateBerryMetadata(versionFetcher versionology.VersionFetcher, platform retrieve.Platform) ([]versionology.Dependency, error) {
	version := versionFetcher.Version().String()

	dependency, err := createBerryDependencyVersion(version, platform)
	if err != nil {
		return nil, fmt.Errorf("could not create berry version: %w", err)
	}

	return []versionology.Dependency{{
		ConfigMetadataDependency: dependency,
		SemverVersion:            versionFetcher.Version(),
	}}, nil
}

func getClassicVersions() (versionology.VersionFetcherArray, error) {
	githubClient := NewGithubClient(NewWebClient())

	classicReleases, err := githubClient.GetReleaseTags("yarnpkg", "yarn")
	if err != nil {
		return nil, fmt.Errorf("could not get classic releases: %w", err)
	}

	var versions []versionology.VersionFetcher
	for _, release := range classicReleases {
		versionTagName := strings.TrimPrefix(release.TagName, "v")
		version, err := semver.NewVersion(versionTagName)
		if err != nil {
			return nil, fmt.Errorf("failed to parse version: %w", err)
		}
		// Skip versions without usable release assets: <0.7.0 lack source/tags;
		// 1.22.20 and 1.22.21 have no binaries.
		if version.LessThan(semver.MustParse("0.7.0")) ||
			version.Equal(semver.MustParse("1.22.20")) ||
			version.Equal(semver.MustParse("1.22.21")) {
			continue
		}
		versions = append(versions, YarnMetadata{version})
	}

	return versions, nil
}

func getBerryVersions() (versionology.VersionFetcherArray, error) {
	githubClient := NewGithubClient(NewWebClient())

	berryReleases, err := githubClient.GetReleaseTags("yarnpkg", "berry")
	if err != nil {
		return nil, fmt.Errorf("could not get berry releases: %w", err)
	}

	var versions []versionology.VersionFetcher
	for _, release := range berryReleases {
		versionStr := strings.TrimPrefix(release.TagName, berryTagPrefix)
		version, err := semver.NewVersion(versionStr)
		if err != nil {
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
		ID:              yarnDependencyID,
		Licenses:        retrieve.LookupLicenses(asset.BrowserDownloadUrl, upstream.DefaultDecompress),
		Name:            "Yarn",
		OS:              platform.OS,
		PURL:            retrieve.GeneratePURL(yarnDependencyID, version, dependencySHA, asset.BrowserDownloadUrl),
		Source:          asset.BrowserDownloadUrl,
		SourceChecksum:  fmt.Sprintf("sha256:%s", dependencySHA),
		StripComponents: 1,
		Stacks:          []string{"io.buildpacks.stacks.bionic", "io.buildpacks.stacks.jammy", "*"},
		URI:             asset.BrowserDownloadUrl,
		Version:         version,
	}, nil
}

// createBerryDependencyVersion builds a ConfigMetadataDependency for a Berry version.
// Downloads the @yarnpkg/cli-dist npm tarball which contains the ready-to-run
// bin/yarn.js bundle (strip-components=1 places bin/ into the layer).
func createBerryDependencyVersion(version string, platform retrieve.Platform) (cargo.ConfigMetadataDependency, error) {
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

	npmMeta, err := getNpmMetadata(webClient, version)
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not get npm registry metadata: %w", err)
	}
	actualSHA1, err := getSHA1(tgzPath)
	if err != nil {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("could not compute SHA1: %w", err)
	}
	if actualSHA1 != npmMeta.Shasum {
		return cargo.ConfigMetadataDependency{}, fmt.Errorf("SHA1 mismatch for cli-dist-%s.tgz: expected %s, got %s", version, npmMeta.Shasum, actualSHA1)
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
		Licenses:        []interface{}{npmMeta.License},
		Name:            "Yarn Berry",
		OS:              platform.OS,
		PURL:            retrieve.GeneratePURL(berryDependencyID, version, dependencySHA, downloadURL),
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

func getSHA1(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	hash := sha1.New()
	_, err = io.Copy(hash, file)
	if err != nil {
		return "", fmt.Errorf("failed to calculate SHA1: %w", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

type npmMetadata struct {
	Shasum  string
	License string
}

// getNpmMetadata fetches shasum and license for a Berry version from the npm registry.
// The cli-dist tarball has no LICENSE file, so license comes from package metadata.
func getNpmMetadata(webClient WebClient, version string) (npmMetadata, error) {
	registryURL := fmt.Sprintf("https://registry.npmjs.org/@yarnpkg/cli-dist/%s", version)
	body, err := webClient.Get(registryURL)
	if err != nil {
		return npmMetadata{}, fmt.Errorf("could not fetch npm registry metadata: %w", err)
	}

	var metadata struct {
		License string `json:"license"`
		Dist    struct {
			Shasum string `json:"shasum"`
		} `json:"dist"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return npmMetadata{}, fmt.Errorf("could not parse npm registry metadata: %w", err)
	}
	if metadata.Dist.Shasum == "" {
		return npmMetadata{}, fmt.Errorf("npm registry did not return a shasum for version %s", version)
	}
	if metadata.License == "" {
		return npmMetadata{}, fmt.Errorf("npm registry did not return a license for version %s", version)
	}

	return npmMetadata{
		Shasum:  metadata.Dist.Shasum,
		License: metadata.License,
	}, nil
}
