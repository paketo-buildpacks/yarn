package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/paketo-buildpacks/occam"
	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"

	. "github.com/onsi/gomega"
)

var settings struct {
	Buildpacks struct {
		BuildPlan struct {
			Online string
		}
		Yarn struct {
			Online  string
			Offline string
		}
	}
	Extensions struct {
		UbiNodejsExtension struct {
			Online string
		}
	}
	Config struct {
		BuildPlan          string `json:"buildplan"`
		UbiNodejsExtension string `json:"ubi-nodejs-extension"`
	}
	Buildpack struct {
		ID   string
		Name string
	}
}

func TestIntegration(t *testing.T) {
	var err error

	Expect := NewWithT(t).Expect

	file, err := os.Open("../integration.json")
	Expect(err).NotTo(HaveOccurred())

	Expect(json.NewDecoder(file).Decode(&settings.Config)).To(Succeed())
	Expect(file.Close()).To(Succeed())

	file, err = os.Open("../buildpack.toml")
	Expect(err).NotTo(HaveOccurred())
	_, err = toml.NewDecoder(file).Decode(&settings)
	Expect(err).NotTo(HaveOccurred())
	Expect(file.Close()).To(Succeed())

	root, err := filepath.Abs("./..")
	Expect(err).ToNot(HaveOccurred())

	buildpackStore := occam.NewBuildpackStore()

	pack := occam.NewPack()

	builder, err := pack.Builder.Inspect.Execute()
	Expect(err).NotTo(HaveOccurred())

	if builder.BuilderName == "paketobuildpacks/builder-ubi8-buildpackless-base" || builder.BuilderName == "paketobuildpacks/ubi-9-builder-buildpackless" {
		settings.Extensions.UbiNodejsExtension.Online, err = buildpackStore.Get.
			Execute(settings.Config.UbiNodejsExtension)
		Expect(err).ToNot(HaveOccurred())
	}

	settings.Buildpacks.Yarn.Online, err = buildpackStore.Get.
		WithVersion("1.2.3").
		Execute(root)
	Expect(err).NotTo(HaveOccurred())

	settings.Buildpacks.Yarn.Offline, err = buildpackStore.Get.
		WithOfflineDependencies().
		WithVersion("1.2.3").
		Execute(root)
	Expect(err).NotTo(HaveOccurred())

	settings.Buildpacks.BuildPlan.Online, err = buildpackStore.Get.
		Execute(settings.Config.BuildPlan)
	Expect(err).NotTo(HaveOccurred())

	SetDefaultEventuallyTimeout(10 * time.Second)

	suite := spec.New("Integration", spec.Report(report.Terminal{}), spec.Parallel())
	suite("Default", testDefault)
	suite("LayerReuse", testRebuildLayerReuse)
	suite("Offline", testOffline)
	suite.Run(t)
}
