package yarn_test

import (
	"testing"

	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"
)

func TestUnitYarn(t *testing.T) {
	suite := spec.New("yarn", spec.Report(report.Terminal{}), spec.Parallel())
	suite("Build", testBuild, spec.Sequential())
	suite("Detect", testDetect)
	suite.Run(t)
}
