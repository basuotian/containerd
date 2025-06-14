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

package native

import (
	"context"
	"runtime"
	"testing"

	"github.com/basuotian/containerd/core/snapshots"
	"github.com/basuotian/containerd/core/snapshots/testsuite"
	"github.com/basuotian/containerd/pkg/testutil"
)

func newSnapshotter(ctx context.Context, root string) (snapshots.Snapshotter, func() error, error) {
	snapshotter, err := NewSnapshotter(root)
	if err != nil {
		return nil, nil, err
	}

	return snapshotter, func() error { return snapshotter.Close() }, nil
}

func TestNative(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Native snapshotter not implemented on windows")
	}
	testutil.RequiresRoot(t)
	testsuite.SnapshotterSuite(t, "Native", newSnapshotter)
}
