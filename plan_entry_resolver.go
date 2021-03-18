package yarn

import (
	"github.com/paketo-buildpacks/packit"
)

type PlanEntryResolver struct{}

func NewPlanEntryResolver() PlanEntryResolver {
	return PlanEntryResolver{}
}

func (r PlanEntryResolver) Resolve(entries []packit.BuildpackPlanEntry) packit.BuildpackPlanEntry {
	entry := entries[0]
	if entry.Metadata == nil {
		entry.Metadata = map[string]interface{}{}
	}

	for _, e := range entries {
		for _, phase := range []string{"build", "launch"} {
			if e.Metadata[phase] == true {
				entry.Metadata[phase] = true
			}
		}
	}

	return entry
}
