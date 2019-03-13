package reporters

import (
	"flag"
	"fmt"

	"github.com/kubernetes-sigs/kubebuilder/pkg/test"
	"github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/config"
	"github.com/onsi/ginkgo/reporters"
)

var (
	// reportDir is used to set the output directory for JUnit artifacts
	reportDir string
)

func init() {
	flag.StringVar(&reportDir, "report-dir", "", "Set report directory for artifact output")
}

// Reporters creates the ginkgo reporters for the test suites
func Reporters() []ginkgo.Reporter {
	reps := []ginkgo.Reporter{test.NewlineReporter{}}
	if reportDir != "" {
		reps = append(reps, reporters.NewJUnitReporter(fmt.Sprintf("%s/junit_%d.xml", reportDir, config.GinkgoConfig.ParallelNode)))
	}
	return reps
}
