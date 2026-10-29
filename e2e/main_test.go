//go:build e2e

package e2e

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/makkalot/caretz/e2e/harness"
	"github.com/makkalot/caretz/e2e/story"
)

// featuresDir holds the .feature files generated from the story tests.
const featuresDir = "features"

func TestMain(m *testing.M) {
	cleanup, err := harness.BuildBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	if code == 0 {
		if err := syncFeatures(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

// syncFeatures regenerates the .feature files when CARETZ_E2E_UPDATE_FEATURES
// is set, and otherwise checks they are current. The check needs every story
// to have run, so it is skipped when -run or -skip narrows the tests.
func syncFeatures() error {
	if os.Getenv("CARETZ_E2E_UPDATE_FEATURES") != "" {
		return story.WriteFeatures(featuresDir)
	}
	if flag.Lookup("test.run").Value.String() != "" || flag.Lookup("test.skip").Value.String() != "" {
		return nil
	}
	return story.CheckFeatures(featuresDir)
}
