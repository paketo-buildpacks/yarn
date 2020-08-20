package yarn_test

import (
	"testing"

	"github.com/paketo-buildpacks/packit"
	"github.com/paketo-buildpacks/yarn"
	"github.com/sclevine/spec"

	. "github.com/onsi/gomega"
)

func testPlanEntryResolver(t *testing.T, context spec.G, it spec.S) {
	var (
		Expect = NewWithT(t).Expect

		resolver yarn.PlanEntryResolver
	)

	it.Before(func() {
		resolver = yarn.NewPlanEntryResolver()
	})

	context("when entry flags differ", func() {
		context("OR's them together on best plan entry", func() {
			it("has all flags", func() {
				entry := resolver.Resolve([]packit.BuildpackPlanEntry{
					{
						Name: "yarn",
						Metadata: map[string]interface{}{
							"launch": true,
						},
					},
					{
						Name: "yarn",
						Metadata: map[string]interface{}{
							"build": true,
						},
					},
				})
				Expect(entry).To(Equal(packit.BuildpackPlanEntry{
					Name: "yarn",
					Metadata: map[string]interface{}{
						"build":  true,
						"launch": true,
					},
				}))
			})
		})
	})
}
