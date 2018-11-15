/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package testlib

import (
	"context"
	"regexp"

	. "github.com/onsi/ginkgo"
	"k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/podlogs"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib/driverlib"
	"k8s.io/kubernetes/test/e2e/storage/testsuites/testlib/patterns"
)

func GetStorageTestFunc(
	frameworkBaseName string,
	prefix string,
	drivers []func() driverlib.TestDriver,
	testSuites []func() TestSuite,
	tunePatternFunc func(patterns []patterns.TestPattern) []patterns.TestPattern,
) func() {
	return func() {
		f := framework.NewDefaultFramework(frameworkBaseName)

		var (
			cancel context.CancelFunc
			cs     clientset.Interface
			ns     *v1.Namespace
			config framework.VolumeTestConfig
		)

		BeforeEach(func() {
			ctx, c := context.WithCancel(context.Background())
			cancel = c
			cs = f.ClientSet
			ns = f.Namespace
			config = framework.VolumeTestConfig{
				Namespace: ns.Name,
				Prefix:    prefix,
			}
			// Debugging of the following tests heavily depends on the log output
			// of the different containers. Therefore include all of that in log
			// files (when using --report-dir, as in the CI) or the output stream
			// (otherwise).
			to := podlogs.LogOutput{
				StatusWriter: GinkgoWriter,
			}
			if framework.TestContext.ReportDir == "" {
				to.LogWriter = GinkgoWriter
			} else {
				test := CurrentGinkgoTestDescription()
				reg := regexp.MustCompile("[^a-zA-Z0-9_-]+")
				// We end the prefix with a slash to ensure that all logs
				// end up in a directory named after the current test.
				to.LogPathPrefix = framework.TestContext.ReportDir + "/" +
					reg.ReplaceAllString(test.FullTestText, "_") + "/"
			}
			podlogs.CopyAllLogs(ctx, cs, ns.Name, to)

			// pod events are something that the framework already collects itself
			// after a failed test. Logging them live is only useful for interactive
			// debugging, not when we collect reports.
			if framework.TestContext.ReportDir == "" {
				podlogs.WatchPods(ctx, cs, ns.Name, GinkgoWriter)
			}
		})

		AfterEach(func() {
			cancel()
		})

		for _, initDriver := range drivers {
			curDriver := initDriver()
			Context(driverlib.GetDriverNameWithFeatureTags(curDriver), func() {
				driver := curDriver

				BeforeEach(func() {
					// setupDriver
					driverlib.SetCommonDriverParameters(driver, f, config)
					driver.CreateDriver()
				})

				AfterEach(func() {
					// Cleanup driver
					driver.CleanupDriver()
				})

				RunTestSuite(f, config, driver, testSuites, tunePatternFunc)
			})
		}
	}
}
