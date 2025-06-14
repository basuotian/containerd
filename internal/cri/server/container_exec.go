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

package server

import (
	"context"
	"fmt"

	"github.com/basuotian/containerd/pkg/tracing"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// Exec prepares a streaming endpoint to execute a command in the container, and returns the address.
func (c *criService) Exec(ctx context.Context, r *runtime.ExecRequest) (*runtime.ExecResponse, error) {
	span := tracing.SpanFromContext(ctx)
	cntr, err := c.containerStore.Get(r.GetContainerId())
	if err != nil {
		return nil, fmt.Errorf("failed to find container %q in store: %w", r.GetContainerId(), err)
	}
	span.SetAttributes(tracing.Attribute("container.id", cntr.ID))
	state := cntr.Status.Get().State()
	if state != runtime.ContainerState_CONTAINER_RUNNING {
		return nil, fmt.Errorf("container is in %s state", criContainerStateToString(state))
	}
	return c.streamServer.GetExec(r)
}
