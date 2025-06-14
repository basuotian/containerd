/*
   Copyright The containerd Authors.

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

package integration

import (
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"

	"github.com/basuotian/containerd/pkg/archive"
)

// downloadPreviousLatestReleaseBinary downloads the latest version of previous
// release into the target dir.
func downloadPreviousLatestReleaseBinary(t *testing.T, version, targetDir string) {
	ver := previousReleaseVersion(t, version)

	downloadReleaseBinary(t, targetDir, ver)
}

// downloadReleaseBinary downloads containerd binary with a given release.
func downloadReleaseBinary(t *testing.T, targetDir string, ver string) {
	targetURL := fmt.Sprintf("https://github.com/containerd/containerd/releases/download/%s/containerd-%s-linux-%s.tar.gz",
		ver, strings.TrimPrefix(ver, "v"), runtime.GOARCH,
	)

	resp, err := http.Get(targetURL) //nolint:gosec
	require.NoError(t, err, "failed to http-get %s", targetURL)

	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	tarReader, err := gzip.NewReader(resp.Body)
	require.NoError(t, err, "%s should be gzip stream", targetURL)

	// NOTE: Use native applier to take release tar.gzip stream as first image layer :)
	_, err = archive.Apply(context.Background(), targetDir, tarReader)
	require.NoError(t, err, "failed to unpack %s gzip stream into %s", targetURL, targetDir)
}

// previousReleaseVersion returns the latest version of previous release.
func previousReleaseVersion(t *testing.T, version string) string {
	tags := gitLsRemoteCtrdTags(t, fmt.Sprintf("refs/tags/v%s.*", version))
	require.True(t, len(tags) >= 1)

	// sort them and get the latest version
	semver.Sort(tags)
	return tags[len(tags)-1]
}

// gitLsRemoteTags lists containerd tags based on pattern.
func gitLsRemoteCtrdTags(t *testing.T, pattern string) (_tags []string) {
	cmd := exec.Command("git", "ls-remote", "--tags", "--exit-code",
		"https://github.com/containerd/containerd.git", pattern)

	t.Logf("Running %s", cmd.String())

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to list tags by pattern %s: %s", pattern, string(out))

	// output is like
	//
	// 137288ad010d39ae6ef578fa53bf9b93d1356c3a        refs/tags/v1.6.8
	// 9cd3357b7fd7218e4aec3eae239db1f68a5a6ec6        refs/tags/v1.6.8^{}
	// cec2382030533cf5797d63a4cdb2b255a9c3c7b6        refs/tags/v1.6.9
	// 1c90a442489720eec95342e1789ee8a5e1b9536f        refs/tags/v1.6.9^{}
	refTags := strings.Fields(string(out))
	require.True(t, len(refTags)%2 == 0)

	tags := make([]string, 0, len(refTags)/2)
	for i := 1; i < len(refTags); i += 2 {
		rawTag := refTags[i]
		require.True(t, strings.HasPrefix(rawTag, "refs/tags/"))

		if strings.HasSuffix(rawTag, "^{}") {
			continue
		}
		rawTag = strings.TrimPrefix(rawTag, "refs/tags/")
		tags = append(tags, rawTag)
	}
	return tags
}
