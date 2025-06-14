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
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	shimcore "github.com/basuotian/containerd/core/runtime/v2"
	"github.com/basuotian/containerd/integration/images"
	"github.com/basuotian/containerd/pkg/namespaces"
	"github.com/basuotian/containerd/pkg/shim"
	apitask "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/ttrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIssue7496 is used to reproduce https://github.com/containerd/containerd/issues/7496
//
// NOTE: https://github.com/containerd/containerd/issues/8931 is the same issue.
func TestIssue7496(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "k8s.io")

	t.Logf("Create a pod config and run sandbox container")
	sbConfig := PodSandboxConfig("sandbox", "issue7496")
	sbID, err := runtimeService.RunPodSandbox(sbConfig, *runtimeHandler)
	require.NoError(t, err)

	shimCli := connectToShim(ctx, t, containerdEndpoint, 3, sbID)

	delayInSec := 12
	t.Logf("[shim pid: %d]: Injecting %d seconds delay to umount2 syscall",
		shimPid(ctx, t, shimCli),
		delayInSec)

	doneCh := injectDelayToUmount2(ctx, t, shimCli, delayInSec /* CRI plugin uses 10 seconds to delete task */)

	t.Logf("Create a container config and run container in a pod")
	pauseImage := images.Get(images.Pause)
	EnsureImageExists(t, pauseImage)

	containerConfig := ContainerConfig("pausecontainer", pauseImage)
	cnID, err := runtimeService.CreateContainer(sbID, containerConfig, sbConfig)
	require.NoError(t, err)
	require.NoError(t, runtimeService.StartContainer(cnID))

	t.Logf("Start to StopPodSandbox and RemovePodSandbox")
	ctx, cancelFn := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelFn()
	for {
		select {
		case <-ctx.Done():
			require.NoError(t, ctx.Err(), "The StopPodSandbox should be done in time")
		default:
		}

		err := runtimeService.StopPodSandbox(sbID)
		if err != nil {
			t.Logf("Failed to StopPodSandbox: %v", err)
			continue
		}

		err = runtimeService.RemovePodSandbox(sbID)
		if err == nil {
			break
		}
		t.Logf("Failed to RemovePodSandbox: %v", err)
		time.Sleep(1 * time.Second)
	}

	t.Logf("PodSandbox %s has been deleted and start to wait for strace exit", sbID)
	select {
	case <-time.After(15 * time.Second):
		resp, err := shimCli.Connect(ctx, &apitask.ConnectRequest{})
		assert.Error(t, err, "should failed to call shim connect API")

		t.Errorf("Strace doesn't exit in time")

		t.Logf("Cleanup the shim (pid: %d)", resp.GetShimPid())
		syscall.Kill(int(resp.GetShimPid()), syscall.SIGKILL)
		<-doneCh
	case <-doneCh:
	}
}

// injectDelayToUmount2 uses strace(1) to inject delay on umount2 syscall to
// simulate IO pressure because umount2 might force kernel to syncfs, for
// example, umount overlayfs rootfs which doesn't with volatile.
//
// REF: https://man7.org/linux/man-pages/man1/strace.1.html
func injectDelayToUmount2(ctx context.Context, t *testing.T, shimCli apitask.TTRPCTaskService, delayInSec int) chan struct{} {
	pid := shimPid(ctx, t, shimCli)

	doneCh := make(chan struct{})

	// use strace command to mock the delay of umount2
	// this require strace version >= 4.22
	cmd := exec.CommandContext(ctx, "strace",
		"-p", strconv.Itoa(int(pid)), "-f", // attach to all the threads
		"-b", "execve", // stop to attach runc child-processes
		"-e", "trace=umount2", // only trace umount2 syscall
		"-e", "inject=umount2:delay_enter="+strconv.Itoa(delayInSec)+"000000",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}

	pipeR, pipeW := io.Pipe()
	cmd.Stdout = pipeW
	cmd.Stderr = pipeW

	require.NoError(t, cmd.Start())

	// ensure that strace has attached to the shim
	readyCh := make(chan struct{})
	go func() {
		defer close(doneCh)

		bufReader := bufio.NewReader(pipeR)
		_, err := bufReader.Peek(1)
		assert.NoError(t, err, "failed to ensure that strace has attached to shim")

		close(readyCh)
		io.Copy(os.Stdout, bufReader)
		t.Logf("Strace has exited")
	}()

	go func() {
		defer pipeW.Close()
		assert.NoError(t, cmd.Wait(), "strace should exit with zero code")
	}()

	<-readyCh
	return doneCh
}

func connectToShim(ctx context.Context, t *testing.T, ctrdEndpoint string, version int, id string) shimcore.TaskServiceClient {
	addr, err := shim.SocketAddress(ctx, ctrdEndpoint, id, false)
	require.NoError(t, err)
	addr = strings.TrimPrefix(addr, "unix://")

	conn, err := net.Dial("unix", addr)
	require.NoError(t, err)

	client := ttrpc.NewClient(conn)
	cli, err := shimcore.NewTaskClient(client, version)
	require.NoError(t, err)
	return cli
}

func shimPid(ctx context.Context, t *testing.T, shimCli shimcore.TaskServiceClient) uint32 {
	resp, err := shimCli.Connect(ctx, &apitask.ConnectRequest{})
	require.NoError(t, err)
	return resp.GetShimPid()
}
