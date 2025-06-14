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

package client

import (
	"context"
	"testing"

	. "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/core/snapshots"
	"github.com/basuotian/containerd/core/snapshots/testsuite"
	"github.com/basuotian/containerd/defaults"
)

func newSnapshotter(ctx context.Context, root string) (snapshots.Snapshotter, func() error, error) {
	client, err := New(address)
	if err != nil {
		return nil, nil, err
	}

	sn := client.SnapshotService(defaults.DefaultSnapshotter)

	return sn, func() error {
		// no need to close remote snapshotter
		return client.Close()
	}, nil
}

func TestSnapshotterClient(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	testsuite.SnapshotterSuite(t, defaults.DefaultSnapshotter, newSnapshotter)
}
