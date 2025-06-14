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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/cgroups/v3"
	"github.com/containerd/cgroups/v3/cgroup1"
	cgroupsv2 "github.com/containerd/cgroups/v3/cgroup2"
	"github.com/containerd/containerd/api/types/runc/options"
	"github.com/containerd/errdefs"
	"github.com/stretchr/testify/assert"

	. "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/core/containers"
	"github.com/basuotian/containerd/integration/failpoint"
	"github.com/basuotian/containerd/integration/images"
	"github.com/basuotian/containerd/pkg/cio"
	"github.com/basuotian/containerd/pkg/fifosync"
	"github.com/basuotian/containerd/pkg/oci"
	"github.com/basuotian/containerd/pkg/shim"
	"github.com/basuotian/containerd/pkg/sys"
	"github.com/basuotian/containerd/plugins"

	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// We use this image for user ns tests because it has files with setuid bits
var testUserNSImage = images.Get(images.VolumeOwnership)

func TestTaskUpdate(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err := client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(32 * 1024 * 1024)
	memory := func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		s.Linux.Resources.Memory = &specs.LinuxMemory{
			Limit: &limit,
		}
		return nil
	}
	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image),
		WithNewSpec(oci.WithImageConfig(image), withProcessArgs("sleep", "30"), memory))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var (
		cgroup  cgroup1.Cgroup
		cgroup2 *cgroupsv2.Manager
	)
	// check that the task has a limit of 32mb
	if cgroups.Mode() == cgroups.Unified {
		groupPath, err := cgroupsv2.PidGroupPath(int(task.Pid()))
		if err != nil {
			t.Fatal(err)
		}
		cgroup2, err = cgroupsv2.Load(groupPath)
		if err != nil {
			t.Fatal(err)
		}
		stat, err := cgroup2.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if int64(stat.Memory.UsageLimit) != limit {
			t.Fatalf("expected memory limit to be set to %d but received %d", limit, stat.Memory.UsageLimit)
		}
	} else {
		cgroup, err = cgroup1.Load(cgroup1.PidPath(int(task.Pid())))
		if err != nil {
			t.Fatal(err)
		}
		stat, err := cgroup.Stat(cgroup1.IgnoreNotExist)
		if err != nil {
			t.Fatal(err)
		}
		if int64(stat.Memory.Usage.Limit) != limit {
			t.Fatalf("expected memory limit to be set to %d but received %d", limit, stat.Memory.Usage.Limit)
		}
	}
	limit = 64 * 1024 * 1024
	if err := task.Update(ctx, WithResources(&specs.LinuxResources{
		Memory: &specs.LinuxMemory{
			Limit: &limit,
		},
	})); err != nil {
		t.Error(err)
	}
	// check that the task has a limit of 64mb
	if cgroups.Mode() == cgroups.Unified {
		stat, err := cgroup2.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if int64(stat.Memory.UsageLimit) != limit {
			t.Errorf("expected memory limit to be set to %d but received %d", limit, stat.Memory.UsageLimit)
		}
	} else {
		stat, err := cgroup.Stat(cgroup1.IgnoreNotExist)
		if err != nil {
			t.Fatal(err)
		}
		if int64(stat.Memory.Usage.Limit) != limit {
			t.Errorf("expected memory limit to be set to %d but received %d", limit, stat.Memory.Usage.Limit)
		}
	}
	if err := task.Kill(ctx, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}

	<-statusC
}

func TestShimInCgroup(t *testing.T) {
	if noShimCgroup {
		t.Skip("shim cgroup is not enabled")
	}

	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var (
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err := client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}
	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), oci.WithProcessArgs("sleep", "30")))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)
	// create a cgroup for the shim to use
	path := "/containerd/shim"
	var (
		cg  cgroup1.Cgroup
		cg2 *cgroupsv2.Manager
	)
	if cgroups.Mode() == cgroups.Unified {
		cg2, err = cgroupsv2.NewManager("/sys/fs/cgroup", path, &cgroupsv2.Resources{})
		if err != nil {
			t.Fatal(err)
		}
		defer cg2.Delete()
	} else {
		cg, err = cgroup1.New(cgroup1.StaticPath(path), &specs.LinuxResources{})
		if err != nil {
			t.Fatal(err)
		}
		defer cg.Delete()
	}

	task, err := container.NewTask(ctx, empty(), WithShimCgroup(path))
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// check to see if the shim is inside the cgroup
	if cgroups.Mode() == cgroups.Unified {
		processes, err := cg2.Procs(false)
		if err != nil {
			t.Fatal(err)
		}
		if len(processes) == 0 {
			t.Errorf("created cgroup should have at least one process inside: %d", len(processes))
		}
	} else {
		processes, err := cg.Processes(cgroup1.Devices, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(processes) == 0 {
			t.Errorf("created cgroup should have at least one process inside: %d", len(processes))
		}
	}
	if err := task.Kill(ctx, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}

	<-statusC
}

func TestShimDoesNotLeakPipes(t *testing.T) {
	containerdPid := ctrd.cmd.Process.Pid
	initialPipes, err := numPipes(containerdPid)
	if err != nil {
		t.Fatal(err)
	}

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), withProcessArgs("sleep", "30")))
	if err != nil {
		t.Fatal(err)
	}

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}

	exitChannel, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	<-exitChannel

	if _, err := task.Delete(ctx); err != nil {
		t.Fatal(err)
	}

	if err := container.Delete(ctx, WithSnapshotCleanup); err != nil {
		t.Fatal(err)
	}

	currentPipes, err := numPipes(containerdPid)
	if err != nil {
		t.Fatal(err)
	}

	if initialPipes != currentPipes {
		t.Errorf("Pipes have leaked after container has been deleted. Initially there were %d pipes, after container deletion there were %d pipes", initialPipes, currentPipes)
	}
}

func TestShimDoesNotLeakSockets(t *testing.T) {
	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), withProcessArgs("sleep", "30")))
	if err != nil {
		t.Fatal(err)
	}

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}

	exitChannel, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	<-exitChannel

	if _, err := task.Delete(ctx); err != nil {
		t.Fatal(err)
	}

	if err := container.Delete(ctx, WithSnapshotCleanup); err != nil {
		t.Fatal(err)
	}

	s, err := shim.SocketAddress(ctx, address, id, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(strings.TrimPrefix(s, "unix://")); err == nil || !os.IsNotExist(err) {
		t.Errorf("Shim sockets have leaked after container has been deleted.")
	}
}

func numPipes(pid int) (int, error) {
	cmd := exec.Command("sh", "-c", fmt.Sprintf("lsof -p %d | grep FIFO", pid))

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	return strings.Count(stdout.String(), "\n"), nil
}

func TestDaemonReconnectsToShimIOPipesOnRestart(t *testing.T) {
	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), withProcessArgs("sleep", "30")))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	_, err = task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if err := ctrd.Restart(nil); err != nil {
		t.Fatal(err)
	}

	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	serving, err := client.IsServing(waitCtx)
	waitCancel()
	if !serving {
		t.Fatalf("containerd did not start within 2s: %v", err)
	}

	// After we restarted containerd we write some messages to the log pipes, simulating shim writing stuff there.
	// Then we make sure that these messages are available on the containerd log thus proving that the server reconnected to the log pipes
	logDirPath := getLogDirPath("v2", id)

	writeToFile(t, filepath.Join(logDirPath, "log"), fmt.Sprintf("%s writing to log\n", id))

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	<-statusC

	stdioContents, err := os.ReadFile(ctrdStdioFilePath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(stdioContents), fmt.Sprintf("%s writing to log", id)) {
		t.Fatal("containerd did not connect to the shim log pipe")
	}
}

func writeToFile(t *testing.T, filePath, message string) {
	writer, err := os.OpenFile(filePath, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString(message); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func getLogDirPath(runtimeVersion, id string) string {
	switch runtimeVersion {
	case "v2":
		return filepath.Join(defaultState, "io.containerd.runtime.v2.task", testNamespace, id)
	default:
		panic(fmt.Errorf("Unsupported runtime version %s", runtimeVersion))
	}
}

func TestContainerAttach(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), withCat()))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	expected := "hello" + newLine

	direct, err := newDirectIO(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Delete()
	var (
		wg  sync.WaitGroup
		buf = bytes.NewBuffer(nil)
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(buf, direct.Stdout)
	}()

	task, err := container.NewTask(ctx, direct.IOCreate)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	status, err := task.Wait(ctx)
	if err != nil {
		t.Error(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := fmt.Fprint(direct.Stdin, expected); err != nil {
		t.Error(err)
	}

	// load the container and re-load the task
	if container, err = client.LoadContainer(ctx, id); err != nil {
		t.Fatal(err)
	}

	if task, err = container.Task(ctx, direct.IOAttach); err != nil {
		t.Fatal(err)
	}

	if _, err := fmt.Fprint(direct.Stdin, expected); err != nil {
		t.Error(err)
	}

	direct.Stdin.Close()

	if err := task.CloseIO(ctx, WithStdinCloser); err != nil {
		t.Error(err)
	}

	<-status

	wg.Wait()
	if _, err := task.Delete(ctx); err != nil {
		t.Error(err)
	}

	output := buf.String()

	// we wrote the same thing after attach
	expected = expected + expected
	if output != expected {
		t.Errorf("expected output %q but received %q", expected, output)
	}
}

func TestContainerUser(t *testing.T) {
	t.Parallel()
	t.Run("UserNameAndGroupName", func(t *testing.T) { testContainerUser(t, "www-data:www-data", "33:33") })
	t.Run("UserIDAndGroupName", func(t *testing.T) { testContainerUser(t, "1001:www-data", "1001:33") })
	t.Run("UserNameAndGroupID", func(t *testing.T) { testContainerUser(t, "www-data:1002", "33:1002") })
	t.Run("UserIDAndGroupID", func(t *testing.T) { testContainerUser(t, "1001:1002", "1001:1002") })
}

func testContainerUser(t *testing.T, userstr, expectedOutput string) {
	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = strings.Replace(t.Name(), "/", "_", -1)
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := newDirectIO(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Delete()
	var (
		wg  sync.WaitGroup
		buf = bytes.NewBuffer(nil)
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(buf, direct.Stdout)
	}()

	container, err := client.NewContainer(ctx, id,
		WithNewSnapshot(id, image),
		WithNewSpec(oci.WithImageConfig(image), oci.WithUser(userstr), oci.WithProcessArgs("sh", "-c", "echo $(id -u):$(id -g)")),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, direct.IOCreate)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-statusC

	wg.Wait()

	output := strings.TrimSuffix(buf.String(), "\n")
	if output != expectedOutput {
		t.Errorf("expected uid:gid to be %q, but received %q", expectedOutput, output)
	}
}

func TestContainerAttachProcess(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), withProcessArgs("sleep", "100")))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	expected := "hello" + newLine

	// creating IO early for easy resource cleanup
	direct, err := newDirectIO(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Delete()
	var (
		wg  sync.WaitGroup
		buf = bytes.NewBuffer(nil)
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(buf, direct.Stdout)
	}()

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	status, err := task.Wait(ctx)
	if err != nil {
		t.Error(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	spec, err := container.Spec(ctx)
	if err != nil {
		t.Fatal(err)
	}

	processSpec := spec.Process
	processSpec.Args = []string{"cat"}
	execID := t.Name() + "_exec"
	process, err := task.Exec(ctx, execID, processSpec, direct.IOCreate)
	if err != nil {
		t.Fatal(err)
	}
	processStatusC, err := process.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := process.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := fmt.Fprint(direct.Stdin, expected); err != nil {
		t.Error(err)
	}

	if process, err = task.LoadProcess(ctx, execID, direct.IOAttach); err != nil {
		t.Fatal(err)
	}

	if _, err := fmt.Fprint(direct.Stdin, expected); err != nil {
		t.Error(err)
	}

	direct.Stdin.Close()

	if err := process.CloseIO(ctx, WithStdinCloser); err != nil {
		t.Error(err)
	}

	<-processStatusC

	wg.Wait()

	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Error(err)
	}

	output := buf.String()

	// we wrote the same thing after attach
	expected = expected + expected
	if output != expected {
		t.Errorf("expected output %q but received %q", expected, output)
	}
	<-status
}

func TestContainerLoadUnexistingProcess(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), withProcessArgs("sleep", "100")))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	// creating IO early for easy resource cleanup
	direct, err := newDirectIO(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Delete()

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	status, err := task.Wait(ctx)
	if err != nil {
		t.Error(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err = task.LoadProcess(ctx, "this-process-does-not-exist", direct.IOAttach); err == nil {
		t.Fatal("an error should have occurred when loading a process that does not exist")
	}

	if !errdefs.IsNotFound(err) {
		t.Fatalf("an error of type NotFound should have been returned when loading a process that does not exist, got %#v instead ", err)
	}

	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Error(err)
	}

	<-status
}

func TestContainerUserID(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := newDirectIO(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Delete()
	var (
		wg  sync.WaitGroup
		buf = bytes.NewBuffer(nil)
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		io.Copy(buf, direct.Stdout)
	}()

	// sys user in the busybox image has a uid and gid of 3.
	container, err := client.NewContainer(ctx, id,
		WithNewSnapshot(id, image),
		WithNewSpec(oci.WithImageConfig(image), oci.WithUserID(3), oci.WithProcessArgs("sh", "-c", "echo $(id -u):$(id -g)")),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, direct.IOCreate)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-statusC

	wg.Wait()

	output := strings.TrimSuffix(buf.String(), "\n")
	if output != "3:3" {
		t.Errorf("expected uid:gid to be 3:3, but received %q", output)
	}
}

func TestContainerKillAll(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id,
		WithNewSnapshot(id, image),
		WithNewSpec(oci.WithImageConfig(image),
			withProcessArgs("sh", "-c", "top"),
			oci.WithHostNamespace(specs.PIDNamespace),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if err := task.Kill(ctx, syscall.SIGKILL, WithKillAll); err != nil {
		t.Error(err)
	}

	<-statusC
	if _, err := task.Delete(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonRestartWithRunningShim(t *testing.T) {
	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}
	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), oci.WithProcessArgs("sleep", "100")))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Error(err)
	}

	pid := task.Pid()
	if pid < 1 {
		t.Fatalf("invalid task pid %d", pid)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}

	var exitStatus ExitStatus
	if err := ctrd.Restart(func() {
		exitStatus = <-statusC
	}); err != nil {
		t.Fatal(err)
	}

	if exitStatus.Error() == nil {
		t.Errorf(`first task.Wait() should have failed with "transport is closing"`)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	c, err := ctrd.waitForStart(waitCtx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	statusC, err = task.Wait(ctx)
	if err != nil {
		t.Error(err)
	}

	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	<-statusC

	if err := unix.Kill(int(pid), 0); err != unix.ESRCH {
		t.Errorf("pid %d still exists", pid)
	}
}

func TestContainerRuntimeOptionsv2(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(
		ctx, id,
		WithNewSnapshot(id, image),
		WithNewSpec(oci.WithImageConfig(image), withExitStatus(7)),
		WithRuntime(plugins.RuntimeRuncV2, &options.Options{BinaryName: "no-runc"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, empty())
	if err == nil {
		t.Errorf("task creation should have failed")
		task.Delete(ctx)
		return
	}
	if !strings.Contains(err.Error(), `"no-runc"`) {
		t.Errorf("task creation should have failed because of lack of executable. Instead failed with: %v", err.Error())
	}

	// It doesn't matter what the NewTaskOpts function is. We are using an existing function in the client package,
	// which will cause the TaskOptions in the new task request to be non-empty.
	// https://github.com/containerd/containerd/issues/11568
	task, err = container.NewTask(ctx, empty(), WithNoNewKeyring)
	if err == nil {
		t.Errorf("task creation should have failed")
		task.Delete(ctx)
		return
	}
	if !strings.Contains(err.Error(), `"no-runc"`) {
		t.Errorf("task creation should have failed because of lack of executable. Instead failed with: %v", err.Error())
	}
}

func TestContainerKillInitPidHost(t *testing.T) {
	initContainerAndCheckChildrenDieOnKill(t, oci.WithHostNamespace(specs.PIDNamespace))
}

func TestUserNamespaces(t *testing.T) {
	for name, test := range map[string]struct {
		testCmd  oci.SpecOpts
		roRootFS bool
		exitCode uint32 // testUserNamespaces validates the exit code of the test container against this value
		uidmaps  []specs.LinuxIDMapping
		gidmaps  []specs.LinuxIDMapping
	}{
		"WritableRootFS": {
			testCmd:  withExitStatus(7),
			roRootFS: false,
			exitCode: 7,
			uidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 1000, Size: 65535}},
			gidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 2000, Size: 65535}},
		},
		// see #1373 and runc#1572
		"ReadonlyRootFS": {
			testCmd:  withExitStatus(7),
			roRootFS: true,
			exitCode: 7,
			uidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 1000, Size: 65535}},
			gidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 2000, Size: 65535}},
		},
		"CheckSetUidBit": {
			testCmd:  withProcessArgs("bash", "-c", "[ -u /usr/bin/passwd ] && exit 7"),
			roRootFS: false,
			exitCode: 7,
			uidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 1000, Size: 65535}},
			gidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 2000, Size: 65535}},
		},
		"WritableRootFSMultipleMap": {
			testCmd:  withExitStatus(7),
			roRootFS: false,
			exitCode: 7,
			uidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 0, Size: 10}, {ContainerID: 10, HostID: 1000, Size: 65535}},
			gidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 0, Size: 20}, {ContainerID: 20, HostID: 2000, Size: 65535}},
		},
		"ReadonlyRootFSMultipleMap": {
			testCmd:  withExitStatus(7),
			roRootFS: true,
			exitCode: 7,
			uidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 0, Size: 20}, {ContainerID: 20, HostID: 2000, Size: 65535}},
			gidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 0, Size: 20}, {ContainerID: 20, HostID: 2000, Size: 65535}},
		},
		"CheckSetUidBitMultipleMap": {
			testCmd:  withProcessArgs("bash", "-c", "[ -u /usr/bin/passwd ] && exit 7"),
			roRootFS: false,
			exitCode: 7,
			uidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 0, Size: 20}, {ContainerID: 20, HostID: 2000, Size: 65535}},
			gidmaps:  []specs.LinuxIDMapping{{ContainerID: 0, HostID: 0, Size: 20}, {ContainerID: 20, HostID: 2000, Size: 65535}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			testUserNamespaces(t, test.uidmaps, test.gidmaps, test.testCmd, test.roRootFS, test.exitCode)
		})
	}
}

func checkUserNS(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER,
	}

	if err := cmd.Run(); err != nil {
		t.Skip("User namespaces are unavailable")
	}
}

func testUserNamespaces(t *testing.T, uidmaps, gidmaps []specs.LinuxIDMapping, cmdOpt oci.SpecOpts, readonlyRootFS bool, expected uint32) {
	checkUserNS(t)

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = strings.Replace(t.Name(), "/", "-", -1)
	)
	defer cancel()

	image, err = client.Pull(ctx, testUserNSImage, WithPullUnpack)
	if err != nil {
		t.Fatal(err)
	}

	opts := []NewContainerOpts{WithNewSpec(oci.WithImageConfig(image),
		cmdOpt,
		oci.WithUserID(34), // run task as the "backup" user
		oci.WithUserNamespace(uidmaps, gidmaps),
	)}

	if readonlyRootFS {
		if len(uidmaps) > 1 {
			opts = append([]NewContainerOpts{WithUserNSRemappedSnapshotView(id, image, uidmaps, gidmaps)}, opts...)
		} else {
			opts = append([]NewContainerOpts{WithRemappedSnapshotView(id, image, 1000, 2000)}, opts...)
		}
	} else {
		if len(uidmaps) > 1 {
			opts = append([]NewContainerOpts{WithUserNSRemappedSnapshot(id, image, uidmaps, gidmaps)}, opts...)
		} else {
			opts = append([]NewContainerOpts{WithRemappedSnapshot(id, image, 1000, 2000)}, opts...)
		}
	}

	container, err := client.NewContainer(ctx, id, opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	copts := &options.Options{
		IoUid: 1000,
		IoGid: 2000,
	}

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio), func(_ context.Context, client *Client, r *TaskInfo) error {
		r.Options = copts
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if pid := task.Pid(); pid < 1 {
		t.Errorf("invalid task pid %d", pid)
	}
	if err := task.Start(ctx); err != nil {
		t.Error(err)
		task.Delete(ctx)
		return
	}
	status := <-statusC
	code, _, err := status.Result()
	if err != nil {
		t.Fatal(err)
	}
	if code != expected {
		t.Errorf("expected status %d from wait but received %d", expected, code)
	}
	deleteStatus, err := task.Delete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ec := deleteStatus.ExitCode(); ec != expected {
		t.Errorf("expected status %d from delete but received %d", expected, ec)
	}
}

func TestUIDNoGID(t *testing.T) {
	t.Parallel()

	ctx, cancel := testContext(t)
	defer cancel()
	id := t.Name()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	image, err := client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithUserID(1000)))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx)

	spec, err := container.Spec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if uid := spec.Process.User.UID; uid != 1000 {
		t.Fatalf("expected uid 1000 but received %d", uid)
	}
	if gid := spec.Process.User.GID; gid != 0 {
		t.Fatalf("expected gid 0 but received %d", gid)
	}
}

func TestBindLowPortNonRoot(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}
	container, err := client.NewContainer(ctx, id,
		WithNewSnapshot(id, image),
		WithNewSpec(oci.WithImageConfig(image), withProcessArgs("nc", "-l", "-p", "80"), oci.WithUIDGID(1000, 1000)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}
	status := <-statusC
	code, _, err := status.Result()
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Errorf("expected status 1 from wait but received %d", code)
	}
	if _, err := task.Delete(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBindLowPortNonOpt(t *testing.T) {
	t.Parallel()

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}
	container, err := client.NewContainer(ctx, id,
		WithNewSnapshot(id, image),
		WithNewSpec(oci.WithImageConfig(image), withProcessArgs("nc", "-l", "-p", "80"), oci.WithUIDGID(1000, 1000), oci.WithAmbientCapabilities([]string{"CAP_NET_BIND_SERVICE"})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, empty())
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(2 * time.Second)
		task.Kill(ctx, unix.SIGTERM)
	}()
	status := <-statusC
	code, _, err := status.Result()
	if err != nil {
		t.Fatal(err)
	}
	// 128 + sigterm
	if code != 143 {
		t.Errorf("expected status 143 from wait but received %d", code)
	}
	if _, err := task.Delete(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestShimOOMScore(t *testing.T) {
	containerdPid := ctrd.cmd.Process.Pid
	containerdScore, err := sys.GetOOMScoreAdj(containerdPid)
	if err != nil {
		t.Fatal(err)
	}

	client, err := newClient(t, address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	path := "/containerd/oomshim"
	var (
		cg  cgroup1.Cgroup
		cg2 *cgroupsv2.Manager
	)
	if cgroups.Mode() == cgroups.Unified {
		cg2, err = cgroupsv2.NewManager("/sys/fs/cgroup", path, &cgroupsv2.Resources{})
		if err != nil {
			t.Fatal(err)
		}
		defer cg2.Delete()
	} else {
		cg, err = cgroup1.New(cgroup1.StaticPath(path), &specs.LinuxResources{})
		if err != nil {
			t.Fatal(err)
		}
		defer cg.Delete()
	}

	image, err = client.GetImage(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}

	container, err := client.NewContainer(ctx, id, WithNewSnapshot(id, image), WithNewSpec(oci.WithImageConfig(image), withProcessArgs("sleep", "30")))
	if err != nil {
		t.Fatal(err)
	}
	defer container.Delete(ctx, WithSnapshotCleanup)

	task, err := container.NewTask(ctx, empty(), WithShimCgroup(path))
	if err != nil {
		t.Fatal(err)
	}
	defer task.Delete(ctx)

	statusC, err := task.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}

	expectedScore := containerdScore + 1
	if expectedScore > sys.OOMScoreAdjMax {
		expectedScore = sys.OOMScoreAdjMax
	}

	// find the shim's pid
	if cgroups.Mode() == cgroups.Unified {
		processes, err := cg2.Procs(false)
		if err != nil {
			t.Fatal(err)
		}
		for _, pid := range processes {
			score, err := sys.GetOOMScoreAdj(int(pid))
			if err != nil {
				t.Fatal(err)
			}
			if score != expectedScore {
				t.Errorf("expected score %d but got %d for shim process", expectedScore, score)
			}
		}
	} else {
		processes, err := cg.Processes(cgroup1.Devices, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range processes {
			score, err := sys.GetOOMScoreAdj(p.Pid)
			if err != nil {
				t.Fatal(err)
			}
			if score != expectedScore {
				t.Errorf("expected score %d but got %d for shim process", expectedScore, score)
			}
		}
	}

	if err := task.Kill(ctx, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}

	select {
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for task exit event")
	case <-statusC:
	}
}

// TestIssue9103 is used as regression case for issue 9103.
//
// The runc-fp will kill the init process so that the shim should return stopped
// status after container.NewTask. It's used to simulate that the runc-init
// might be killed by oom-kill.
func TestIssue9103(t *testing.T) {
	if f := os.Getenv("RUNC_FLAVOR"); f != "" && f != "runc" {
		t.Skip("test requires runc")
	}

	client, err := newClient(t, address)
	require.NoError(t, err)
	defer client.Close()

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	defer cancel()

	image, err = client.GetImage(ctx, testImage)
	require.NoError(t, err)

	for idx, tc := range []struct {
		desc           string
		cntrOpts       []NewContainerOpts
		bakingFn       func(ctx context.Context, t *testing.T, task Task)
		expectedStatus ProcessStatus
	}{
		{
			desc: "should be created status",
			cntrOpts: []NewContainerOpts{
				WithNewSpec(oci.WithImageConfig(image),
					withProcessArgs("sleep", "30"),
				),
			},
			bakingFn:       func(context.Context, *testing.T, Task) {},
			expectedStatus: Created,
		},
		{
			desc: "should be stopped status if init has been killed",
			cntrOpts: []NewContainerOpts{
				WithNewSpec(oci.WithImageConfig(image),
					withProcessArgs("sleep", "30"),
					oci.WithAnnotations(map[string]string{
						"oci.runc.failpoint.profile": "issue9103",
					}),
				),
				WithRuntime(client.Runtime(), &options.Options{
					BinaryName: "runc-fp",
				}),
			},
			bakingFn: func(ctx context.Context, t *testing.T, task Task) {
				waitCh, err := task.Wait(ctx)
				require.NoError(t, err)

				select {
				case <-time.After(30 * time.Second):
					t.Fatal("timeout")
				case e := <-waitCh:
					require.NoError(t, e.Error())
				}
			},
			expectedStatus: Stopped,
		},
	} {
		tName := fmt.Sprintf("%s%d", id, idx)
		t.Run(tc.desc, func(t *testing.T) {
			container, err := client.NewContainer(ctx, tName,
				append([]NewContainerOpts{WithNewSnapshot(tName, image)}, tc.cntrOpts...)...,
			)
			require.NoError(t, err)
			defer container.Delete(ctx, WithSnapshotCleanup)

			cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
			task, err := container.NewTask(cctx, empty())
			ccancel()
			require.NoError(t, err)

			defer task.Delete(ctx, WithProcessKill)

			tc.bakingFn(ctx, t, task)

			status, err := task.Status(ctx)
			require.NoError(t, err)
			require.Equal(t, tc.expectedStatus, status.Status)
		})
	}
}

// TestIssue10589 is used as regression case for issue 10589.
//
// This issue was caused by a race between init exits and new exec process tracking inside the shim.  The test operates
// by controlling the time between when the shim invokes "runc exec" and when the actual "runc exec" is triggered.  This
// allows validating that races for shim state tracking between pre- and post-start of the exec process do not exist.
//
// The workflow is as follows:
// 1. Create a container as normal
// 2. Make an exec1 using runc-fp with delayexec
// 3. Wait until the exec is waiting to start (triggered by delayexec)
// 4. Kill the container init process (signalling it is easiest)
// 5. Make an exec2 using runc-fp with delayexec
// 6. Wait until the exec is waiting to start
// 7. Allow exec1 to proceed
// 8. Allow exec2 to proceed
// 9. See that the container has exited and all execs have exited too
//
// https://github.com/containerd/containerd/issues/10589
func TestIssue10589(t *testing.T) {
	if f := os.Getenv("RUNC_FLAVOR"); f != "" && f != "runc" {
		t.Skip("test requires runc")
	}

	client, err := newClient(t, address)
	require.NoError(t, err)
	t.Cleanup(func() {
		client.Close()
	})

	var (
		image       Image
		ctx, cancel = testContext(t)
		id          = t.Name()
	)
	t.Cleanup(cancel)

	image, err = client.GetImage(ctx, testImage)
	require.NoError(t, err)

	// 1. Create a sleeping container
	t.Log("1. Create a sleeping container")
	container, err := client.NewContainer(ctx, id,
		WithNewSnapshot(id, image),
		WithNewSpec(oci.WithImageConfig(image),
			withProcessArgs("sleep", "inf"),
			oci.WithAnnotations(map[string]string{
				"oci.runc.failpoint.profile": "delayExec",
			}),
		),
		WithRuntime(client.Runtime(), &options.Options{
			BinaryName: "runc-fp",
		}),
	)
	require.NoError(t, err, "create container")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := container.Delete(ctx, WithSnapshotCleanup)
		if err != nil {
			t.Log("delete err", err)
		}
		cancel()
	})

	task, err := container.NewTask(ctx, empty())
	require.NoError(t, err, "create task")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		st, err := task.Delete(ctx, WithProcessKill)
		t.Log("exit status", st)
		if err != nil {
			t.Log("kill err", err)
		}
		cancel()
	})

	err = task.Start(ctx)
	require.NoError(t, err, "start container")

	status, err := task.Status(ctx)
	require.NoError(t, err, "container status")
	require.Equal(t, Running, status.Status)

	// 2. Create an exec
	t.Log("2. Create exec1")
	exec1ReadyFifo, err := fifosync.NewWaiter(filepath.Join(t.TempDir(), "exec1-ready.fifo"), 0600)
	require.NoError(t, err, "create exec1 ready fifo")
	exec1DelayFifo, err := fifosync.NewTrigger(filepath.Join(t.TempDir(), "exec1-delay.fifo"), 0600)
	require.NoError(t, err, "create exec1 delay fifo")
	exec1, err := task.Exec(ctx, "exec1", &specs.Process{
		Args: []string{"/bin/sleep", "301"},
		Cwd:  "/",
		Env: []string{
			failpoint.DelayExecReadyEnv + "=" + exec1ReadyFifo.Name(),
			failpoint.DelayExecDelayEnv + "=" + exec1DelayFifo.Name(),
		},
	}, cio.NullIO)
	require.NoError(t, err, "create exec1")

	exec1done := make(chan struct{})
	go func() {
		defer close(exec1done)
		t.Log("Starting exec1")
		err := exec1.Start(ctx)
		assert.Error(t, err, "start exec1")
		t.Logf("error starting exec1: %s", err)
	}()

	// 3. Wait until the exec is waiting to start
	t.Log("3. Wait until exec1 is waiting to start")
	err = exec1ReadyFifo.Wait()
	require.NoError(t, err, "open exec1 fifo")

	// 4. Kill the container init process
	t.Log("4. Kill the container init process")
	target := task.Pid()
	t.Logf("Killing main pid (%v) of container %s", target, container.ID())
	syscall.Kill(int(target), syscall.SIGKILL)
	status, err = task.Status(ctx)
	require.NoError(t, err, "container status")
	t.Log("container status", status.Status)

	// 5. Make an exec (2) using this failpoint
	t.Log("5. Create exec2")
	exec2ReadyFifo, err := fifosync.NewWaiter(filepath.Join(t.TempDir(), "exec2-ready.fifo"), 0600)
	require.NoError(t, err, "create exec2 ready fifo: %q", exec2ReadyFifo)
	exec2DelayFifo, err := fifosync.NewTrigger(filepath.Join(t.TempDir(), "exec2-delay.fifo"), 0600)
	require.NoError(t, err, "create exec2 delay fifo: %q", exec2DelayFifo)
	exec2, err := task.Exec(ctx, "exec2", &specs.Process{
		Args: []string{"/bin/sleep", "302"},
		Cwd:  "/",
		Env: []string{
			failpoint.DelayExecReadyEnv + "=" + exec2ReadyFifo.Name(),
			failpoint.DelayExecDelayEnv + "=" + exec2DelayFifo.Name(),
		},
	}, cio.NullIO)
	require.NoError(t, err, "create exec2")

	exec2done := make(chan struct{})
	didExec2Run := true
	go func() {
		defer close(exec2done)
		t.Log("Starting exec2")
		err := exec2.Start(ctx)
		assert.Error(t, err, "start exec2")
		t.Logf("error starting exec2: %s", err)
	}()

	// 6. Wait until the exec is waiting to start
	t.Log("6. Wait until exec2 is waiting to start")
	exec2ready := make(chan struct{})
	go func() {
		exec2ReadyFifo.Wait()
		close(exec2ready)
	}()
	select {
	case <-exec2ready:
	case <-exec2done:
		didExec2Run = false
	}

	// 7. Allow exec=1 to proceed
	t.Log("7. Allow exec=1 to proceed")
	err = exec1DelayFifo.Trigger()
	assert.NoError(t, err, "trigger exec1 fifo")
	status, err = task.Status(ctx)
	require.NoError(t, err, "container status")
	t.Log("container status", status.Status)
	<-exec1done
	status, err = task.Status(ctx)
	require.NoError(t, err, "container status")
	t.Log("container status", status.Status)

	// 8. Allow exec=2 to proceed
	if didExec2Run {
		t.Log("8. Allow exec2 to proceed")
		err = exec2DelayFifo.Trigger()
		assert.NoError(t, err, "trigger exec2 fifo")
		status, err = task.Status(ctx)
		require.NoError(t, err, "container status")
		t.Log("container status", status.Status)
		<-exec2done
		status, err = task.Status(ctx)
		require.NoError(t, err, "container status")
		t.Log("container status", status.Status)
	} else {
		t.Log("8. Skip exec2")
	}

	// 9. Validate
	t.Log("9. Validate")
	status, err = exec1.Status(ctx)
	require.NoError(t, err, "exec1 status")
	t.Logf("exec1 status: %s", status.Status)
	assert.Equal(t, Created, status.Status)
	status, err = exec2.Status(ctx)
	require.NoError(t, err, "exec2 status")
	t.Logf("exec2 status: %s", status.Status)
	assert.Equal(t, Created, status.Status)
	status, err = task.Status(ctx)
	t.Logf("task status: %s", status.Status)
	require.NoError(t, err, "container status")
	assert.Equal(t, Stopped, status.Status)
}
