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

package podsandbox

import (
	"context"
	"fmt"
	goruntime "runtime"
	"time"

	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"

	containerd "github.com/basuotian/containerd/client"
	sandbox2 "github.com/basuotian/containerd/core/sandbox"
	"github.com/basuotian/containerd/internal/cri/config"
	"github.com/basuotian/containerd/internal/cri/server/podsandbox/types"
	sandboxstore "github.com/basuotian/containerd/internal/cri/store/sandbox"
	ctrdutil "github.com/basuotian/containerd/internal/cri/util"
	"github.com/basuotian/containerd/pkg/netns"
)

// loadContainerTimeout is the default timeout for loading a container/sandbox.
// One container/sandbox hangs (e.g. containerd#2438) should not affect other
// containers/sandboxes.
// Most CRI container/sandbox related operations are per container, the ones
// which handle multiple containers at a time are:
// * ListPodSandboxes: Don't talk with containerd services.
// * ListContainers: Don't talk with containerd services.
// * ListContainerStats: Not in critical code path, a default timeout will
// be applied at CRI level.
// * Recovery logic: We should set a time for each container/sandbox recovery.
// * Event monitor: We should set a timeout for each container/sandbox event handling.
const loadContainerTimeout = 10 * time.Second

func (c *Controller) RecoverContainer(ctx context.Context, cntr containerd.Container) (sandboxstore.Sandbox, error) {
	ctx, cancel := context.WithTimeout(ctx, loadContainerTimeout)
	defer cancel()
	var sandbox sandboxstore.Sandbox
	meta, err := getMetadata(ctx, cntr)
	if err != nil {
		return sandbox, err
	}

	// Load sandbox created timestamp.
	info, err := cntr.Info(ctx)
	if err != nil {
		return sandbox, fmt.Errorf("failed to get sandbox container info: %w", err)
	}

	s, ch, err := func() (sandboxstore.Status, <-chan containerd.ExitStatus, error) {
		status := sandboxstore.Status{
			State: sandboxstore.StateUnknown,
		}
		var channel <-chan containerd.ExitStatus

		status.CreatedAt = info.CreatedAt

		// Load sandbox state.
		t, err := cntr.Task(ctx, nil)
		if err != nil && !errdefs.IsNotFound(err) {
			return status, channel, fmt.Errorf("failed to load task: %w", err)
		}
		var taskStatus containerd.Status
		var notFound bool
		if errdefs.IsNotFound(err) {
			// Task is not found.
			notFound = true
		} else {
			// Task is found. Get task status.
			taskStatus, err = t.Status(ctx)
			if err != nil {
				// It's still possible that task is deleted during this window.
				if !errdefs.IsNotFound(err) {
					return status, channel, fmt.Errorf("failed to get task status: %w", err)
				}
				notFound = true
			}
		}
		if notFound {
			// Task does not exist, set sandbox state as NOTREADY.
			status.State = sandboxstore.StateNotReady
		} else {
			if taskStatus.Status == containerd.Running {
				exitCh, err := t.Wait(ctrdutil.NamespacedContext())
				if err != nil {
					if !errdefs.IsNotFound(err) {
						return status, channel, fmt.Errorf("failed to wait for sandbox container task: %w", err)
					}
					status.State = sandboxstore.StateNotReady
				} else {
					status.State = sandboxstore.StateReady
					status.Pid = t.Pid()
					channel = exitCh
				}
			} else {
				// Task is not running. Delete the task and set sandbox state as NOTREADY.
				if _, err := t.Delete(ctx, containerd.WithProcessKill); err != nil && !errdefs.IsNotFound(err) {
					return status, channel, fmt.Errorf("failed to delete task: %w", err)
				}
				status.State = sandboxstore.StateNotReady
			}
		}
		return status, channel, nil
	}()
	if err != nil {
		log.G(ctx).WithError(err).Errorf("Failed to load sandbox status for %q", cntr.ID())
	}

	// save it to cache in the podsandbox controller
	podSandbox := types.NewPodSandbox(cntr.ID(), s)
	podSandbox.Container = cntr
	if meta != nil {
		podSandbox.Metadata = *meta
	}
	podSandbox.Runtime = sandbox2.RuntimeOpts{
		Name:    info.Runtime.Name,
		Options: info.Runtime.Options,
	}
	if ch != nil {
		go func() {
			if err := c.waitSandboxExit(ctrdutil.NamespacedContext(), podSandbox, ch); err != nil {
				log.G(context.Background()).Warnf("failed to wait pod sandbox exit %v", err)
			}
		}()
	}

	if err := c.store.Save(podSandbox); err != nil {
		return sandbox, fmt.Errorf("failed to save pod sandbox container in mem store: %w", err)
	}

	sandbox = sandboxstore.NewSandbox(*meta, s)
	sandbox.Container = cntr
	sandbox.Sandboxer = string(config.ModePodSandbox)

	// Load network namespace.
	sandbox.NetNS = getNetNS(meta)

	// It doesn't matter whether task is running or not. If it is running, sandbox
	// status will be `READY`; if it is not running, sandbox status will be `NOT_READY`,
	// kubelet will stop the sandbox which will properly cleanup everything.
	return sandbox, nil
}

func getNetNS(meta *sandboxstore.Metadata) *netns.NetNS {
	// Don't need to load netns for host network sandbox.
	if hostNetwork(meta.Config) {
		return nil
	}
	return netns.LoadNetNS(meta.NetNSPath)
}

// hostNetwork handles checking if host networking was requested.
// TODO: Copy pasted from sbserver to handle container sandbox events in podsandbox/ package, needs refactoring.
func hostNetwork(config *runtime.PodSandboxConfig) bool {
	var hostNet bool
	switch goruntime.GOOS {
	case "windows":
		// Windows HostProcess pods can only run on the host network
		hostNet = config.GetWindows().GetSecurityContext().GetHostProcess()
	case "darwin":
		// No CNI on Darwin yet.
		hostNet = true
	default:
		// Even on other platforms, the logic containerd uses is to check if NamespaceMode == NODE.
		// So this handles Linux, as well as any other platforms not governed by the cases above
		// that have special quirks.
		hostNet = config.GetLinux().GetSecurityContext().GetNamespaceOptions().GetNetwork() == runtime.NamespaceMode_NODE
	}
	return hostNet
}
