package yarn

import "github.com/paketo-buildpacks/packit"

func Build() packit.BuildFunc {
	return func(context packit.BuildContext) (packit.BuildResult, error) {
		yarnLayer, err := context.Layers.Get(YarnLayerName)
		if err != nil {
			return packit.BuildResult{}, err
		}
		return packit.BuildResult{
			Plan: packit.BuildpackPlan{
				Entries: []packit.BuildpackPlanEntry{
					{
						Name: YarnDependency,
					},
				},
			},
			Layers: []packit.Layer{yarnLayer},
		}, nil
	}
}
