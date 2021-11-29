module github.com/paketo-buildpacks/yarn

go 1.16

require (
	github.com/BurntSushi/toml v0.4.1
	github.com/onsi/gomega v1.17.0
	github.com/paketo-buildpacks/occam v0.1.4
	github.com/paketo-buildpacks/packit v1.3.1
	github.com/sclevine/spec v1.4.0
)

replace github.com/paketo-buildpacks/packit => /home/ubuntu/workspace/paketo-buildpacks/yarn/../packit

replace github.com/anchore/syft => github.com/jonasagx/syft v0.27.1-0.20211118073839-eee29112ef6a
