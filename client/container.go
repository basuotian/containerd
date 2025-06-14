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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/api/types/runc/options"
	tasktypes "github.com/containerd/containerd/api/types/task"
	"github.com/containerd/errdefs"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/fifo"
	"github.com/containerd/typeurl/v2"
	ver "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/selinux/go-selinux/label"

	"github.com/basuotian/containerd/core/containers"
	"github.com/basuotian/containerd/core/images"
	"github.com/basuotian/containerd/pkg/cio"
	"github.com/basuotian/containerd/pkg/oci"
	"github.com/basuotian/containerd/pkg/tracing"
)

const (
	checkpointRuntimeNameLabel     = "io.containerd.checkpoint.runtime"
	checkpointSnapshotterNameLabel = "io.containerd.checkpoint.snapshotter"
)

// Container is a metadata object for container resources and task creation
type Container interface {
	// ID identifies the container
	ID() string
	// Info returns the underlying container record type
	Info(context.Context, ...InfoOpts) (containers.Container, error)
	// Delete removes the container
	Delete(context.Context, ...DeleteOpts) error
	// NewTask creates a new task based on the container metadata
	NewTask(context.Context, cio.Creator, ...NewTaskOpts) (Task, error)
	// Spec returns the OCI runtime specification
	Spec(context.Context) (*oci.Spec, error)
	// Task returns the current task for the container
	//
	// If cio.Load options are passed the client will Load the IO for the running
	// task.
	//
	// If cio.Attach options are passed the client will reattach to the IO for the running
	// task.
	//
	// If no task exists for the container a NotFound error is returned
	//
	// Clients must make sure that only one reader is attached to the task and consuming
	// the output from the task's fifos
	Task(context.Context, cio.Attach) (Task, error)
	// Image returns the image that the container is based on
	Image(context.Context) (Image, error)
	// Labels returns the labels set on the container
	Labels(context.Context) (map[string]string, error)
	// SetLabels sets the provided labels for the container and returns the final label set
	SetLabels(context.Context, map[string]string) (map[string]string, error)
	// Extensions returns the extensions set on the container
	Extensions(context.Context) (map[string]typeurl.Any, error)
	// Update a container
	Update(context.Context, ...UpdateContainerOpts) error
	// Checkpoint creates a checkpoint image of the current container
	Checkpoint(context.Context, string, ...CheckpointOpts) (Image, error)
	// Restore restores a container and returns the PID of the
	// restored containers init process.
	Restore(context.Context, cio.Creator, string) (int, error)
}

func containerFromRecord(client *Client, c containers.Container) *container {
	return &container{
		client:   client,
		id:       c.ID,
		metadata: c,
	}
}

var _ = (Container)(&container{})

type container struct {
	client   *Client
	id       string
	metadata containers.Container
}

// ID returns the container's unique id
func (c *container) ID() string {
	return c.id
}

func (c *container) Info(ctx context.Context, opts ...InfoOpts) (containers.Container, error) {
	i := &InfoConfig{
		// default to refreshing the container's local metadata
		Refresh: true,
	}
	for _, o := range opts {
		o(i)
	}
	if i.Refresh {
		metadata, err := c.get(ctx)
		if err != nil {
			return c.metadata, err
		}
		c.metadata = metadata
	}
	return c.metadata, nil
}

func (c *container) Extensions(ctx context.Context) (map[string]typeurl.Any, error) {
	r, err := c.get(ctx)
	if err != nil {
		return nil, err
	}
	return r.Extensions, nil
}

func (c *container) Labels(ctx context.Context) (map[string]string, error) {
	r, err := c.get(ctx)
	if err != nil {
		return nil, err
	}
	return r.Labels, nil
}

func (c *container) SetLabels(ctx context.Context, labels map[string]string) (map[string]string, error) {
	ctx, span := tracing.StartSpan(ctx, "container.SetLabels",
		tracing.WithAttribute("container.id", c.id),
	)
	defer span.End()
	container := containers.Container{
		ID:     c.id,
		Labels: labels,
	}

	var paths []string
	// mask off paths so we only muck with the labels encountered in labels.
	// Labels not in the passed in argument will be left alone.
	for k := range labels {
		paths = append(paths, strings.Join([]string{"labels", k}, "."))
	}

	r, err := c.client.ContainerService().Update(ctx, container, paths...)
	if err != nil {
		return nil, err
	}
	return r.Labels, nil
}

// Spec returns the current OCI specification for the container
func (c *container) Spec(ctx context.Context) (*oci.Spec, error) {
	r, err := c.get(ctx)
	if err != nil {
		return nil, err
	}
	var s oci.Spec
	if err := json.Unmarshal(r.Spec.GetValue(), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Delete deletes an existing container
// an error is returned if the container has running tasks
func (c *container) Delete(ctx context.Context, opts ...DeleteOpts) error {
	ctx, span := tracing.StartSpan(ctx, "container.Delete",
		tracing.WithAttribute("container.id", c.id),
	)
	defer span.End()
	if _, err := c.loadTask(ctx, nil); err == nil {
		return fmt.Errorf("cannot delete running task %v: %w", c.id, errdefs.ErrFailedPrecondition)
	}
	r, err := c.get(ctx)
	if err != nil {
		return err
	}
	for _, o := range opts {
		if err := o(ctx, c.client, r); err != nil {
			return err
		}
	}
	return c.client.ContainerService().Delete(ctx, c.id)
}

func (c *container) Task(ctx context.Context, attach cio.Attach) (Task, error) {
	return c.loadTask(ctx, attach)
}

// Image returns the image that the container is based on
func (c *container) Image(ctx context.Context) (Image, error) {
	r, err := c.get(ctx)
	if err != nil {
		return nil, err
	}
	if r.Image == "" {
		return nil, fmt.Errorf("container not created from an image: %w", errdefs.ErrNotFound)
	}
	i, err := c.client.ImageService().Get(ctx, r.Image)
	if err != nil {
		return nil, fmt.Errorf("failed to get image %s for container: %w", r.Image, err)
	}
	return NewImage(c.client, i), nil
}

func (c *container) NewTask(ctx context.Context, ioCreate cio.Creator, opts ...NewTaskOpts) (_ Task, retErr error) {
	ctx, span := tracing.StartSpan(ctx, "container.NewTask")
	defer span.End()
	i, err := ioCreate(c.id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if retErr != nil && i != nil {
			i.Cancel()
			i.Close()
		}
	}()
	cfg := i.Config()
	request := &tasks.CreateTaskRequest{
		ContainerID: c.id,
		Terminal:    cfg.Terminal,
		Stdin:       cfg.Stdin,
		Stdout:      cfg.Stdout,
		Stderr:      cfg.Stderr,
	}
	if err := c.handleMounts(ctx, request); err != nil {
		return nil, err
	}

	r, err := c.get(ctx)
	if err != nil {
		return nil, err
	}
	info := TaskInfo{
		runtime:        r.Runtime.Name,
		runtimeOptions: r.Runtime.Options,
	}
	for _, o := range opts {
		if err := o(ctx, c.client, &info); err != nil {
			return nil, err
		}
	}
	for _, m := range info.RootFS {
		request.Rootfs = append(request.Rootfs, &types.Mount{
			Type:    m.Type,
			Source:  m.Source,
			Target:  m.Target,
			Options: m.Options,
		})
	}
	request.RuntimePath = info.RuntimePath
	if info.Options != nil {
		o, err := typeurl.MarshalAny(info.Options)
		if err != nil {
			return nil, err
		}
		request.Options = typeurl.MarshalProto(o)
	}
	t := &task{
		client: c.client,
		io:     i,
		id:     c.id,
		c:      c,
	}
	if info.Checkpoint != nil {
		request.Checkpoint = info.Checkpoint
	}

	span.SetAttributes(
		tracing.Attribute("task.container.id", request.ContainerID),
		tracing.Attribute("task.request.options", request.Options.String()),
		tracing.Attribute("task.runtime.name", info.runtime),
	)
	response, err := c.client.TaskService().Create(ctx, request)
	if err != nil {
		return nil, errgrpc.ToNative(err)
	}

	span.AddEvent("task created",
		tracing.Attribute("task.process.id", int(response.Pid)),
	)
	t.pid = response.Pid
	return t, nil
}

func (c *container) Update(ctx context.Context, opts ...UpdateContainerOpts) error {
	// fetch the current container config before updating it
	ctx, span := tracing.StartSpan(ctx, "container.Update")
	defer span.End()
	r, err := c.get(ctx)
	if err != nil {
		return err
	}
	for _, o := range opts {
		if err := o(ctx, c.client, &r); err != nil {
			return err
		}
	}
	if _, err := c.client.ContainerService().Update(ctx, r); err != nil {
		return errgrpc.ToNative(err)
	}
	return nil
}

func (c *container) handleMounts(ctx context.Context, request *tasks.CreateTaskRequest) error {
	r, err := c.get(ctx)
	if err != nil {
		return err
	}

	if r.SnapshotKey != "" {
		if r.Snapshotter == "" {
			return fmt.Errorf("unable to resolve rootfs mounts without snapshotter on container: %w", errdefs.ErrInvalidArgument)
		}

		// get the rootfs from the snapshotter and add it to the request
		s, err := c.client.getSnapshotter(ctx, r.Snapshotter)
		if err != nil {
			return err
		}
		mounts, err := s.Mounts(ctx, r.SnapshotKey)
		if err != nil {
			return err
		}
		spec, err := c.Spec(ctx)
		if err != nil {
			return err
		}
		for _, m := range mounts {
			if spec.Linux != nil && spec.Linux.MountLabel != "" {
				if ml := label.FormatMountLabel("", spec.Linux.MountLabel); ml != "" {
					m.Options = append(m.Options, ml)
				}
			}
			request.Rootfs = append(request.Rootfs, &types.Mount{
				Type:    m.Type,
				Source:  m.Source,
				Target:  m.Target,
				Options: m.Options,
			})
		}
	}

	return nil
}

func (c *container) Restore(ctx context.Context, ioCreate cio.Creator, rootDir string) (_ int, retErr error) {
	errorPid := -1
	i, err := ioCreate(c.id)
	if err != nil {
		return errorPid, err
	}
	defer func() {
		if retErr != nil && i != nil {
			i.Cancel()
			i.Close()
		}
	}()
	cfg := i.Config()

	request := &tasks.CreateTaskRequest{
		ContainerID: c.id,
		Terminal:    cfg.Terminal,
		Stdin:       cfg.Stdin,
		Stdout:      cfg.Stdout,
		Stderr:      cfg.Stderr,
	}

	if err := c.handleMounts(ctx, request); err != nil {
		return errorPid, err
	}

	request.Checkpoint = &types.Descriptor{
		Annotations: map[string]string{
			// The following annotation is used to restore a checkpoint
			// via CRI. This is mainly used to restore a container
			// in Kubernetes.
			"RestoreFromPath": rootDir,
		},
	}
	// (adrianreber): it is not totally clear to me, but it seems the only
	// way to restore a container in containerd is going through Create().
	// This functions sets up Create() in such a way to handle container
	// restore coming through the CRI.
	response, err := c.client.TaskService().Create(ctx, request)
	if err != nil {
		return errorPid, errgrpc.ToNative(err)
	}

	return int(response.GetPid()), nil
}

func (c *container) Checkpoint(ctx context.Context, ref string, opts ...CheckpointOpts) (Image, error) {
	index := &ocispec.Index{
		Versioned: ver.Versioned{
			SchemaVersion: 2,
		},
		Annotations: make(map[string]string),
	}
	copts := &options.CheckpointOptions{
		Exit:                false,
		OpenTcp:             false,
		ExternalUnixSockets: false,
		Terminal:            false,
		FileLocks:           true,
		EmptyNamespaces:     nil,
	}
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}

	img, err := c.Image(ctx)
	if err != nil {
		return nil, err
	}

	ctx, done, err := c.client.WithLease(ctx)
	if err != nil {
		return nil, err
	}
	defer done(ctx)

	// add image name to manifest
	index.Annotations[ocispec.AnnotationRefName] = img.Name()
	// add runtime info to index
	index.Annotations[checkpointRuntimeNameLabel] = info.Runtime.Name
	// add snapshotter info to index
	index.Annotations[checkpointSnapshotterNameLabel] = info.Snapshotter

	// process remaining opts
	for _, o := range opts {
		if err := o(ctx, c.client, &info, index, copts); err != nil {
			err = errgrpc.ToNative(err)
			if !errdefs.IsAlreadyExists(err) {
				return nil, err
			}
		}
	}

	desc, err := writeIndex(ctx, index, c.client, c.ID()+"index")
	if err != nil {
		return nil, err
	}
	i := images.Image{
		Name:   ref,
		Target: desc,
	}
	checkpoint, err := c.client.ImageService().Create(ctx, i)
	if err != nil {
		return nil, err
	}

	return NewImage(c.client, checkpoint), nil
}

func (c *container) loadTask(ctx context.Context, ioAttach cio.Attach) (Task, error) {
	response, err := c.client.TaskService().Get(ctx, &tasks.GetRequest{
		ContainerID: c.id,
	})
	if err != nil {
		err = errgrpc.ToNative(err)
		if errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("no running task found: %w", err)
		}
		return nil, err
	}
	var i cio.IO
	if ioAttach != nil && response.Process.Status != tasktypes.Status_UNKNOWN {
		// Do not attach IO for task in unknown state, because there
		// are no fifo paths anyway.
		if i, err = attachExistingIO(response, ioAttach); err != nil {
			return nil, err
		}
	}
	t := &task{
		client: c.client,
		io:     i,
		id:     response.Process.ID,
		pid:    response.Process.Pid,
		c:      c,
	}
	return t, nil
}

func (c *container) get(ctx context.Context) (containers.Container, error) {
	return c.client.ContainerService().Get(ctx, c.id)
}

// get the existing fifo paths from the task information stored by the daemon
func attachExistingIO(response *tasks.GetResponse, ioAttach cio.Attach) (cio.IO, error) {
	fifoSet := loadFifos(response)
	return ioAttach(fifoSet)
}

// loadFifos loads the containers fifos
func loadFifos(response *tasks.GetResponse) *cio.FIFOSet {
	fifos := []string{
		response.Process.Stdin,
		response.Process.Stdout,
		response.Process.Stderr,
	}
	closer := func() error {
		var (
			err  error
			dirs = map[string]struct{}{}
		)
		for _, f := range fifos {
			if isFifo, _ := fifo.IsFifo(f); isFifo {
				if rerr := os.Remove(f); err == nil {
					err = rerr
				}
				dirs[filepath.Dir(f)] = struct{}{}
			}
		}
		for dir := range dirs {
			// we ignore errors here because we don't
			// want to remove the directory if it isn't
			// empty
			_ = os.Remove(dir)
		}
		return err
	}

	return cio.NewFIFOSet(cio.Config{
		Stdin:    response.Process.Stdin,
		Stdout:   response.Process.Stdout,
		Stderr:   response.Process.Stderr,
		Terminal: response.Process.Terminal,
	}, closer)
}
