package yarn_test

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/paketo-buildpacks/go-dist/fakes"
	"github.com/paketo-buildpacks/packit"
	"github.com/paketo-buildpacks/yarn"
	"github.com/sclevine/spec"

	. "github.com/onsi/gomega"
)

func testBuild(t *testing.T, context spec.G, it spec.S) {
	var (
		Expect = NewWithT(t).Expect

		layersDir     string
		workingDir    string
		cnbDir        string
		entryResolver *fakes.EntryResolver

		build packit.BuildFunc
	)

	it.Before(func() {
		var err error
		layersDir, err = ioutil.TempDir("", "layers")
		Expect(err).NotTo(HaveOccurred())

		cnbDir, err = ioutil.TempDir("", "cnb")
		Expect(err).NotTo(HaveOccurred())

		workingDir, err = ioutil.TempDir("", "working-dir")
		Expect(err).NotTo(HaveOccurred())

		build = yarn.Build()
	})

	it.After(func() {
		Expect(os.RemoveAll(layersDir)).To(Succeed())
		Expect(os.RemoveAll(cnbDir)).To(Succeed())
		Expect(os.RemoveAll(workingDir)).To(Succeed())
	})

	it("returns a result that installs yarn", func() {
		result, err := build(packit.BuildContext{
			WorkingDir: workingDir,
			CNBPath:    cnbDir,
			Stack:      "some-stack",
			BuildpackInfo: packit.BuildpackInfo{
				Name:    "Some Buildpack",
				Version: "some-version",
			},
			Plan: packit.BuildpackPlan{
				Entries: []packit.BuildpackPlanEntry{},
			},
			Layers: packit.Layers{Path: layersDir},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(result).To(Equal(packit.BuildResult{
			Plan: packit.BuildpackPlan{
				Entries: []packit.BuildpackPlanEntry{
					{
						Name: "yarn",
					},
				},
			},
			Layers: []packit.Layer{
				{
					Name:      "yarn",
					Path:      filepath.Join(layersDir, "yarn"),
					SharedEnv: packit.Environment{},
					BuildEnv:  packit.Environment{},
					LaunchEnv: packit.Environment{},
					Build:     false,
					Launch:    false,
					Cache:     false,
				},
			},
		}))
	})

	context("when the plan entry requires the dependency during the build an launch phases", func() {
		it.Before(func() {
			entryResolver.ResolveCall.Returns.BuildpackPlanEntry = packit.BuildpackPlanEntry{
				Name: "yarn",
				Metadata: map[string]interface{}{
					"build":  true,
					"launch": true,
				},
			}
		})
		it("makes the layer available in those phases", func() {
			result, err := build(packit.BuildContext{
				CNBPath: cnbDir,
				Plan: packit.BuildpackPlan{
					Entries: []packit.BuildpackPlanEntry{
						{
							Name: "yarn",
							Metadata: map[string]interface{}{
								"build":  true,
								"launch": true,
							},
						},
					},
				},
				Layers: packit.Layers{Path: layersDir},
				Stack:  "some-stack",
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(result).To(Equal(packit.BuildResult{
				Plan: packit.BuildpackPlan{
					Entries: []packit.BuildpackPlanEntry{
						{
							Name: "yarn",
						},
					},
				},
				Layers: []packit.Layer{
					{
						Name:      "yarn",
						Path:      filepath.Join(layersDir, "yarn"),
						SharedEnv: packit.Environment{},
						BuildEnv:  packit.Environment{},
						LaunchEnv: packit.Environment{},
						Build:     true,
						Launch:    true,
						Cache:     true,
					},
				},
			}))
		})
	})

	context("failure cases", func() {
		context("when the yarn layer cannot be retrieved", func() {
			it.Before(func() {
				err := ioutil.WriteFile(filepath.Join(layersDir, "yarn.toml"), nil, 0000)
				Expect(err).NotTo(HaveOccurred())
			})

			it("returns an error", func() {
				_, err := build(packit.BuildContext{
					CNBPath: cnbDir,
					Plan: packit.BuildpackPlan{
						Entries: []packit.BuildpackPlanEntry{
							{Name: "yarn"},
						},
					},
					Layers: packit.Layers{Path: layersDir},
					Stack:  "some-stack",
				})
				Expect(err).To(MatchError(ContainSubstring("failed to parse layer content metadata")))
			})

		})
	})
}
