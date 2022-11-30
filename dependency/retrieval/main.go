package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/joshuatcasey/libdependency/retrieve"
	"github.com/joshuatcasey/libdependency/upstream"
	"github.com/joshuatcasey/libdependency/versionology"
	"github.com/paketo-buildpacks/packit/v2/cargo"
	"golang.org/x/crypto/openpgp"
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
	retrieve.NewMetadata("yarn", getAllVersions, generateMetadata)
}

func generateMetadata(versionFetcher versionology.VersionFetcher) ([]versionology.Dependency, error) {
	version := versionFetcher.Version().String()
	releases, err := NewGithubClient(NewWebClient()).GetReleaseTags("yarnpkg", "yarn")
	if err != nil {
		return nil, fmt.Errorf("could not get releases: %w", err)
	}

	for _, release := range releases {
		tagName := "v" + version
		if release.TagName == tagName {
			dependency, err := createDependencyVersion(version, tagName, release)
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

func createDependencyVersion(version, tagName string, release GithubRelease) (cargo.ConfigMetadataDependency, error) {
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
		CPE:             fmt.Sprintf("cpe:2.3:a:yarnpkg:yarn:%s:*:*:*:*:*:*:*", version),
		Checksum:        fmt.Sprintf("sha256:%s", dependencySHA),
		ID:              "yarn",
		Licenses:        retrieve.LookupLicenses(asset.BrowserDownloadUrl, upstream.DefaultDecompress),
		Name:            "Yarn",
		PURL:            retrieve.GeneratePURL("yarn", version, dependencySHA, asset.BrowserDownloadUrl),
		Source:          asset.BrowserDownloadUrl,
		SourceChecksum:  fmt.Sprintf("sha256:%s", dependencySHA),
		Stacks:          []string{"io.buildpacks.stacks.bionic", "io.buildpacks.stacks.jammy"},
		URI:             asset.BrowserDownloadUrl,
		Version:         version,
		DeprecationDate: nil,
		StripComponents: 1,
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

		_, err = openpgp.CheckArmoredDetachedSignature(keyring, file, strings.NewReader(asc))
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
