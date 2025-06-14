//go:build linux

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

package overlay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	containerd "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/core/mount"
	"github.com/basuotian/containerd/core/snapshots"
	"github.com/basuotian/containerd/core/snapshots/storage"
	"github.com/basuotian/containerd/core/snapshots/testsuite"
	"github.com/basuotian/containerd/internal/userns"
	"github.com/basuotian/containerd/pkg/testutil"
	"github.com/basuotian/containerd/plugins/snapshots/overlay/overlayutils"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func newSnapshotterWithOpts(opts ...Opt) testsuite.SnapshotterFunc {
	return func(ctx context.Context, root string) (snapshots.Snapshotter, func() error, error) {
		snapshotter, err := NewSnapshotter(root, opts...)
		if err != nil {
			return nil, nil, err
		}

		return snapshotter, func() error { return snapshotter.Close() }, nil
	}
}

func TestOverlay(t *testing.T) {
	testutil.RequiresRoot(t)
	optTestCases := map[string][]Opt{
		"no opt": nil,
		// default in init()
		"AsynchronousRemove": {AsynchronousRemove},
		// idmapped mounts enabled
		"WithRemapIDs": {WithRemapIDs},
	}

	for optsName, opts := range optTestCases {
		t.Run(optsName, func(t *testing.T) {
			newSnapshotter := newSnapshotterWithOpts(opts...)
			testsuite.SnapshotterSuite(t, "overlayfs", newSnapshotter)
			t.Run("TestOverlayRemappedBind", func(t *testing.T) {
				testOverlayRemappedBind(t, newSnapshotter)
			})
			t.Run("TestOverlayRemappedActive", func(t *testing.T) {
				testOverlayRemappedActive(t, newSnapshotter)
			})
			t.Run("TestOverlayRemappedInvalidMappings", func(t *testing.T) {
				testOverlayRemappedInvalidMapping(t, newSnapshotter)
			})
			t.Run("TestOverlayMounts", func(t *testing.T) {
				testOverlayMounts(t, newSnapshotter)
			})
			t.Run("TestOverlayCommit", func(t *testing.T) {
				testOverlayCommit(t, newSnapshotter)
			})
			t.Run("TestOverlayOverlayMount", func(t *testing.T) {
				testOverlayOverlayMount(t, newSnapshotter)
			})
			t.Run("TestOverlayOverlayRead", func(t *testing.T) {
				testOverlayOverlayRead(t, newSnapshotter)
			})
			t.Run("TestOverlayView", func(t *testing.T) {
				testOverlayView(t, newSnapshotterWithOpts(append(opts, WithMountOptions([]string{"volatile"}))...))
			})
		})
	}
}

func testOverlayMounts(t *testing.T, newSnapshotter testsuite.SnapshotterFunc) {
	ctx := context.TODO()
	root := t.TempDir()
	o, _, err := newSnapshotter(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	mounts, err := o.Prepare(ctx, "/tmp/test", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 {
		t.Errorf("should only have 1 mount but received %d", len(mounts))
	}
	m := mounts[0]
	if m.Type != "bind" {
		t.Errorf("mount type should be bind but received %q", m.Type)
	}
	expected := filepath.Join(root, "snapshots", "1", "fs")
	if m.Source != expected {
		t.Errorf("expected source %q but received %q", expected, m.Source)
	}
	if m.Options[0] != "rw" {
		t.Errorf("expected mount option rw but received %q", m.Options[0])
	}
	if m.Options[1] != "rbind" {
		t.Errorf("expected mount option rbind but received %q", m.Options[1])
	}
}

func testOverlayCommit(t *testing.T, newSnapshotter testsuite.SnapshotterFunc) {
	ctx := context.TODO()
	root := t.TempDir()
	o, _, err := newSnapshotter(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	key := "/tmp/test"
	mounts, err := o.Prepare(ctx, key, "")
	if err != nil {
		t.Fatal(err)
	}
	m := mounts[0]
	if err := os.WriteFile(filepath.Join(m.Source, "foo"), []byte("hi"), 0660); err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(ctx, "base", key); err != nil {
		t.Fatal(err)
	}
}

func testOverlayOverlayMount(t *testing.T, newSnapshotter testsuite.SnapshotterFunc) {
	ctx := context.TODO()
	root := t.TempDir()
	o, _, err := newSnapshotter(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	key := "/tmp/test"
	if _, err = o.Prepare(ctx, key, ""); err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(ctx, "base", key); err != nil {
		t.Fatal(err)
	}
	var mounts []mount.Mount
	if mounts, err = o.Prepare(ctx, "/tmp/layer2", "base"); err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 {
		t.Errorf("should only have 1 mount but received %d", len(mounts))
	}
	m := mounts[0]
	if m.Type != "overlay" {
		t.Errorf("mount type should be overlay but received %q", m.Type)
	}
	if m.Source != "overlay" {
		t.Errorf("expected source %q but received %q", "overlay", m.Source)
	}
	var (
		expected []string
		bp       = getBasePath(ctx, o, root, "/tmp/layer2")
		work     = "workdir=" + filepath.Join(bp, "work")
		upper    = "upperdir=" + filepath.Join(bp, "fs")
		lower    = "lowerdir=" + getParents(ctx, o, root, "/tmp/layer2")[0]
	)

	expected = append(expected, []string{
		work,
		upper,
		lower,
	}...)

	if supportsIndex() {
		expected = append(expected, "index=off")
	}
	if userxattr, err := overlayutils.NeedsUserXAttr(root); err != nil {
		t.Fatal(err)
	} else if userxattr {
		expected = append(expected, "userxattr")
	}

	for i, v := range expected {
		if m.Options[i] != v {
			t.Errorf("expected %q but received %q", v, m.Options[i])
		}
	}
}

func testOverlayRemappedBind(t *testing.T, newSnapshotter testsuite.SnapshotterFunc) {
	for _, test := range []struct {
		idMap        userns.IDMap
		snapOpt      func(idMap userns.IDMap) snapshots.Opt
		expUID       uint32
		expGID       uint32
		expUIDMntOpt string
		expGIDMntOpt string
	}{
		{
			idMap: userns.IDMap{
				UidMap: []specs.LinuxIDMapping{
					{
						ContainerID: 0,
						HostID:      666,
						Size:        65536,
					},
				},
				GidMap: []specs.LinuxIDMapping{
					{
						ContainerID: 0,
						HostID:      666,
						Size:        65536,
					},
				},
			},
			snapOpt: func(idMap userns.IDMap) snapshots.Opt {
				return containerd.WithRemapperLabels(
					idMap.UidMap[0].ContainerID, idMap.UidMap[0].HostID,
					idMap.GidMap[0].ContainerID, idMap.GidMap[0].HostID,
					idMap.UidMap[0].Size,
				)
			},
			expUID:       666,
			expGID:       666,
			expUIDMntOpt: "uidmap=0:666:65536",
			expGIDMntOpt: "gidmap=0:666:65536",
		},
		{
			idMap: userns.IDMap{
				UidMap: []specs.LinuxIDMapping{
					{
						ContainerID: 0,
						HostID:      666,
						Size:        1000,
					},
					{
						ContainerID: 1000,
						HostID:      6666,
						Size:        64536,
					},
				},
				GidMap: []specs.LinuxIDMapping{
					{
						ContainerID: 0,
						HostID:      888,
						Size:        1000,
					},
					{
						ContainerID: 1000,
						HostID:      8888,
						Size:        64536,
					},
				},
			},
			snapOpt: func(idMap userns.IDMap) snapshots.Opt {
				return containerd.WithUserNSRemapperLabels(idMap.UidMap, idMap.GidMap)
			},
			expUID:       666,
			expGID:       888,
			expUIDMntOpt: "uidmap=0:666:1000,1000:6666:64536",
			expGIDMntOpt: "gidmap=0:888:1000,1000:8888:64536",
		},
	} {
		var (
			opts   []snapshots.Opt
			mounts []mount.Mount
		)

		ctx := context.TODO()
		root := t.TempDir()
		o, _, err := newSnapshotter(ctx, root)
		if err != nil {
			t.Fatal(err)
		}

		if sn, ok := o.(*snapshotter); !ok || !sn.remapIDs {
			t.Skip("overlayfs doesn't support idmapped mounts")
		}

		opts = append(opts, test.snapOpt(test.idMap))

		key := "/tmp/test"
		if mounts, err = o.Prepare(ctx, key, "", opts...); err != nil {
			t.Fatal(err)
		}

		bp := getBasePath(ctx, o, root, key)
		expected := []string{test.expUIDMntOpt, test.expGIDMntOpt, "rw", "rbind"}

		checkMountOpts := func() {
			if len(mounts) != 1 {
				t.Errorf("should only have 1 mount but received %d", len(mounts))
			}

			if len(mounts[0].Options) != len(expected) {
				t.Errorf("expected %d options, but received %d", len(expected), len(mounts[0].Options))
			}

			m := mounts[0]
			for i, v := range expected {
				if m.Options[i] != v {
					t.Errorf("mount option %q is not valid, expected %q", m.Options[i], v)
				}
			}

			st, err := os.Stat(filepath.Join(bp, "fs"))
			if err != nil {
				t.Errorf("failed to stat %s", filepath.Join(bp, "fs"))
			}

			if stat, ok := st.Sys().(*syscall.Stat_t); !ok {
				t.Errorf("incompatible types after stat call: *syscall.Stat_t expected")
			} else if stat.Uid != test.expUID || stat.Gid != test.expGID {
				t.Errorf("bad mapping: expected {uid: %d, gid: %d}; real {uid: %d, gid: %d}", test.expUID, test.expGID, int(stat.Uid), int(stat.Gid))
			}
		}
		checkMountOpts()

		expected[2] = "ro"
		if err = o.Commit(ctx, "base", key, opts...); err != nil {
			t.Fatal(err)
		}
		if mounts, err = o.View(ctx, key, "base", opts...); err != nil {
			t.Fatal(err)
		}
		bp = getBasePath(ctx, o, root, key)
		checkMountOpts()

		key = "/tmp/test1"
		if mounts, err = o.Prepare(ctx, key, ""); err != nil {
			t.Fatal(err)
		}

		bp = getBasePath(ctx, o, root, key)

		expected = expected[2:]
		expected[0] = "rw"

		test.expUID = 0
		test.expGID = 0

		checkMountOpts()
	}
}

func testOverlayRemappedActive(t *testing.T, newSnapshotter testsuite.SnapshotterFunc) {
	for _, test := range []struct {
		idMap        userns.IDMap
		snapOpt      func(idMap userns.IDMap) snapshots.Opt
		expUID       uint32
		expGID       uint32
		expUIDMntOpt string
		expGIDMntOpt string
	}{
		{
			idMap: userns.IDMap{
				UidMap: []specs.LinuxIDMapping{
					{
						ContainerID: 0,
						HostID:      666,
						Size:        65536,
					},
				},
				GidMap: []specs.LinuxIDMapping{
					{
						ContainerID: 0,
						HostID:      666,
						Size:        65536,
					},
				},
			},
			snapOpt: func(idMap userns.IDMap) snapshots.Opt {
				return containerd.WithRemapperLabels(
					idMap.UidMap[0].ContainerID, idMap.UidMap[0].HostID,
					idMap.GidMap[0].ContainerID, idMap.GidMap[0].HostID,
					idMap.UidMap[0].Size,
				)
			},
			expUID:       666,
			expGID:       666,
			expUIDMntOpt: "uidmap=0:666:65536",
			expGIDMntOpt: "gidmap=0:666:65536",
		},
		{
			idMap: userns.IDMap{
				UidMap: []specs.LinuxIDMapping{
					{
						ContainerID: 0,
						HostID:      666,
						Size:        1000,
					},
					{
						ContainerID: 1000,
						HostID:      6666,
						Size:        64536,
					},
				},
				GidMap: []specs.LinuxIDMapping{
					{
						ContainerID: 0,
						HostID:      888,
						Size:        1000,
					},
					{
						ContainerID: 1000,
						HostID:      8888,
						Size:        64536,
					},
				},
			},
			snapOpt: func(idMap userns.IDMap) snapshots.Opt {
				return containerd.WithUserNSRemapperLabels(idMap.UidMap, idMap.GidMap)
			},
			expUID:       666,
			expGID:       888,
			expUIDMntOpt: "uidmap=0:666:1000,1000:6666:64536",
			expGIDMntOpt: "gidmap=0:888:1000,1000:8888:64536",
		},
	} {
		var (
			opts   []snapshots.Opt
			mounts []mount.Mount
		)

		ctx := context.TODO()
		root := t.TempDir()
		o, _, err := newSnapshotter(ctx, root)
		if err != nil {
			t.Fatal(err)
		}

		if sn, ok := o.(*snapshotter); !ok || !sn.remapIDs {
			t.Skip("overlayfs doesn't support idmapped mounts")
		}

		opts = append(opts, test.snapOpt(test.idMap))

		key := "/tmp/test"
		if _, err = o.Prepare(ctx, key, "", opts...); err != nil {
			t.Fatal(err)
		}
		if err = o.Commit(ctx, "base", key, opts...); err != nil {
			t.Fatal(err)
		}
		if mounts, err = o.Prepare(ctx, key, "base", opts...); err != nil {
			t.Fatal(err)
		}

		if len(mounts) != 1 {
			t.Errorf("should only have 1 mount but received %d", len(mounts))
		}

		bp := getBasePath(ctx, o, root, key)
		expected := []string{
			test.expUIDMntOpt, test.expGIDMntOpt,
			fmt.Sprintf("workdir=%s", filepath.Join(bp, "work")),
			fmt.Sprintf("upperdir=%s", filepath.Join(bp, "fs")),
			fmt.Sprintf("lowerdir=%s", getParents(ctx, o, root, key)[0]),
		}

		m := mounts[0]
		for i, v := range expected {
			if m.Options[i] != v {
				t.Errorf("mount option %q is invalid, expected %q", m.Options[i], v)
			}
		}

		st, err := os.Stat(filepath.Join(bp, "fs"))
		if err != nil {
			t.Errorf("failed to stat %s", filepath.Join(bp, "fs"))
		}
		if stat, ok := st.Sys().(*syscall.Stat_t); !ok {
			t.Errorf("incompatible types after stat call: *syscall.Stat_t expected")
		} else if stat.Uid != test.expUID || stat.Gid != test.expGID {
			t.Errorf("bad mapping: expected {uid: %d, gid: %d}; received {uid: %d, gid: %d}", test.expUID, test.expGID, int(stat.Uid), int(stat.Gid))
		}
	}
}

func testOverlayRemappedInvalidMapping(t *testing.T, newSnapshotter testsuite.SnapshotterFunc) {
	ctx := context.TODO()
	root := t.TempDir()
	o, _, err := newSnapshotter(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	if sn, ok := o.(*snapshotter); !ok || !sn.remapIDs {
		t.Skip("overlayfs doesn't support idmapped mounts")
	}

	key := "/tmp/test"
	for desc, opts := range map[string][]snapshots.Opt{
		"WithLabels: negative UID mapping must fail": {
			snapshots.WithLabels(map[string]string{
				snapshots.LabelSnapshotUIDMapping: "-1:-1:-2",
				snapshots.LabelSnapshotGIDMapping: "0:0:66666",
			}),
		},
		"WithLabels: negative GID mapping must fail": {
			snapshots.WithLabels(map[string]string{
				snapshots.LabelSnapshotUIDMapping: "0:0:66666",
				snapshots.LabelSnapshotGIDMapping: "-1:-1:-2",
			}),
		},
		"WithLabels: negative GID/UID mappings must fail": {
			snapshots.WithLabels(map[string]string{
				snapshots.LabelSnapshotUIDMapping: "-666:-666:-666",
				snapshots.LabelSnapshotGIDMapping: "-666:-666:-666",
			}),
		},
		"WithLabels: negative UID in multiple mappings must fail": {
			snapshots.WithLabels(map[string]string{
				snapshots.LabelSnapshotUIDMapping: "1:1:1,-1:-1:-2",
				snapshots.LabelSnapshotGIDMapping: "0:0:66666",
			}),
		},
		"WithLabels: negative GID in multiple mappings must fail": {
			snapshots.WithLabels(map[string]string{
				snapshots.LabelSnapshotUIDMapping: "0:0:66666",
				snapshots.LabelSnapshotGIDMapping: "-1:-1:-2,6:6:6",
			}),
		},
		"WithLabels: negative GID/UID in multiple mappings must fail": {
			snapshots.WithLabels(map[string]string{
				snapshots.LabelSnapshotUIDMapping: "-666:-666:-666,1:1:1",
				snapshots.LabelSnapshotGIDMapping: "-666:-666:-666,2:2:2",
			}),
		},
		"WithRemapperLabels: container ID (GID/UID) other than 0 must fail": {
			containerd.WithRemapperLabels(666, 666, 666, 666, 666),
		},
		"WithRemapperLabels: container ID (UID) other than 0 must fail": {
			containerd.WithRemapperLabels(666, 0, 0, 0, 65536),
		},
		"WithRemapperLabels: container ID (GID) other than 0 must fail": {
			containerd.WithRemapperLabels(0, 0, 666, 0, 4294967295),
		},
		"WithUserNSRemapperLabels: container ID (GID/UID) other than 0 must fail": {
			containerd.WithUserNSRemapperLabels(
				[]specs.LinuxIDMapping{{ContainerID: 666, HostID: 666, Size: 666}},
				[]specs.LinuxIDMapping{{ContainerID: 666, HostID: 666, Size: 666}},
			),
		},
		"WithUserNSRemapperLabels: container ID (UID) other than 0 must fail": {
			containerd.WithUserNSRemapperLabels(
				[]specs.LinuxIDMapping{{ContainerID: 666, HostID: 0, Size: 65536}},
				[]specs.LinuxIDMapping{{ContainerID: 0, HostID: 0, Size: 65536}},
			),
		},
		"WithUserNSRemapperLabels: container ID (GID) other than 0 must fail": {
			containerd.WithUserNSRemapperLabels(
				[]specs.LinuxIDMapping{{ContainerID: 0, HostID: 0, Size: 4294967295}},
				[]specs.LinuxIDMapping{{ContainerID: 666, HostID: 0, Size: 4294967295}},
			),
		},
	} {
		t.Log(desc)
		if _, err = o.Prepare(ctx, key, "", opts...); err == nil {
			t.Fatalf("snapshots with invalid mappings must fail")
		}
		// remove may fail, but it doesn't matter
		_ = o.Remove(ctx, key)
	}
}

func getBasePath(ctx context.Context, sn snapshots.Snapshotter, root, key string) string {
	o := sn.(*snapshotter)
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		panic(err)
	}
	defer t.Rollback()

	s, err := storage.GetSnapshot(ctx, key)
	if err != nil {
		panic(err)
	}

	return filepath.Join(root, "snapshots", s.ID)
}

func getParents(ctx context.Context, sn snapshots.Snapshotter, root, key string) []string {
	o := sn.(*snapshotter)
	ctx, t, err := o.ms.TransactionContext(ctx, false)
	if err != nil {
		panic(err)
	}
	defer t.Rollback()
	s, err := storage.GetSnapshot(ctx, key)
	if err != nil {
		panic(err)
	}
	parents := make([]string, len(s.ParentIDs))
	for i := range s.ParentIDs {
		parents[i] = filepath.Join(root, "snapshots", s.ParentIDs[i], "fs")
	}
	return parents
}

func testOverlayOverlayRead(t *testing.T, newSnapshotter testsuite.SnapshotterFunc) {
	testutil.RequiresRoot(t)
	ctx := context.TODO()
	root := t.TempDir()
	o, _, err := newSnapshotter(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	key := "/tmp/test"
	mounts, err := o.Prepare(ctx, key, "")
	if err != nil {
		t.Fatal(err)
	}
	m := mounts[0]
	if err := os.WriteFile(filepath.Join(m.Source, "foo"), []byte("hi"), 0660); err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(ctx, "base", key); err != nil {
		t.Fatal(err)
	}
	if mounts, err = o.Prepare(ctx, "/tmp/layer2", "base"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "dest")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := mount.All(mounts, dest); err != nil {
		t.Fatal(err)
	}
	defer syscall.Unmount(dest, 0)
	data, err := os.ReadFile(filepath.Join(dest, "foo"))
	if err != nil {
		t.Fatal(err)
	}
	if e := string(data); e != "hi" {
		t.Fatalf("expected file contents hi but got %q", e)
	}
}

func testOverlayView(t *testing.T, newSnapshotter testsuite.SnapshotterFunc) {
	ctx := context.TODO()
	root := t.TempDir()
	o, _, err := newSnapshotter(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	key := "/tmp/base"
	mounts, err := o.Prepare(ctx, key, "")
	if err != nil {
		t.Fatal(err)
	}
	m := mounts[0]
	if err := os.WriteFile(filepath.Join(m.Source, "foo"), []byte("hi"), 0660); err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(ctx, "base", key); err != nil {
		t.Fatal(err)
	}

	key = "/tmp/top"
	_, err = o.Prepare(ctx, key, "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(getParents(ctx, o, root, "/tmp/top")[0], "foo"), []byte("hi, again"), 0660); err != nil {
		t.Fatal(err)
	}
	if err := o.Commit(ctx, "top", key); err != nil {
		t.Fatal(err)
	}

	mounts, err = o.View(ctx, "/tmp/view1", "base")
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 {
		t.Fatalf("should only have 1 mount but received %d", len(mounts))
	}
	m = mounts[0]
	if m.Type != "bind" {
		t.Errorf("mount type should be bind but received %q", m.Type)
	}
	expected := getParents(ctx, o, root, "/tmp/view1")[0]
	if m.Source != expected {
		t.Errorf("expected source %q but received %q", expected, m.Source)
	}

	if m.Options[0] != "ro" {
		t.Errorf("expected mount option ro but received %q", m.Options[0])
	}
	if m.Options[1] != "rbind" {
		t.Errorf("expected mount option rbind but received %q", m.Options[1])
	}

	mounts, err = o.View(ctx, "/tmp/view2", "top")
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 {
		t.Fatalf("should only have 1 mount but received %d", len(mounts))
	}
	m = mounts[0]
	if m.Type != "overlay" {
		t.Errorf("mount type should be overlay but received %q", m.Type)
	}
	if m.Source != "overlay" {
		t.Errorf("mount source should be overlay but received %q", m.Source)
	}

	supportsIndex := supportsIndex()
	expectedOptions := 3
	if !supportsIndex {
		expectedOptions--
	}
	userxattr, err := overlayutils.NeedsUserXAttr(root)
	if err != nil {
		t.Fatal(err)
	}
	if userxattr {
		expectedOptions++
	}

	if len(m.Options) != expectedOptions {
		t.Errorf("expected %d additional mount option but got %d", expectedOptions, len(m.Options))
	}
	lowers := getParents(ctx, o, root, "/tmp/view2")

	expected = fmt.Sprintf("lowerdir=%s:%s", lowers[0], lowers[1])
	if m.Options[0] != expected {
		t.Errorf("expected option %q but received %q", expected, m.Options[0])
	}

	if m.Options[1] != "volatile" {
		t.Error("expected option first option to be provided option \"volatile\"")
	}
}
