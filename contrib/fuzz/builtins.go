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

package fuzz

import (
	// base containerd imports
	_ "github.com/basuotian/containerd/core/runtime/v2"
	_ "github.com/basuotian/containerd/plugins/cri"
	_ "github.com/basuotian/containerd/plugins/cri/images"
	_ "github.com/basuotian/containerd/plugins/cri/runtime"
	_ "github.com/basuotian/containerd/plugins/diff/walking/plugin"
	_ "github.com/basuotian/containerd/plugins/events"
	_ "github.com/basuotian/containerd/plugins/gc"
	_ "github.com/basuotian/containerd/plugins/imageverifier"
	_ "github.com/basuotian/containerd/plugins/leases"
	_ "github.com/basuotian/containerd/plugins/metadata"
	_ "github.com/basuotian/containerd/plugins/nri"
	_ "github.com/basuotian/containerd/plugins/restart"
	_ "github.com/basuotian/containerd/plugins/sandbox"
	_ "github.com/basuotian/containerd/plugins/services/containers"
	_ "github.com/basuotian/containerd/plugins/services/content"
	_ "github.com/basuotian/containerd/plugins/services/diff"
	_ "github.com/basuotian/containerd/plugins/services/events"
	_ "github.com/basuotian/containerd/plugins/services/healthcheck"
	_ "github.com/basuotian/containerd/plugins/services/images"
	_ "github.com/basuotian/containerd/plugins/services/introspection"
	_ "github.com/basuotian/containerd/plugins/services/leases"
	_ "github.com/basuotian/containerd/plugins/services/namespaces"
	_ "github.com/basuotian/containerd/plugins/services/opt"
	_ "github.com/basuotian/containerd/plugins/services/sandbox"
	_ "github.com/basuotian/containerd/plugins/services/snapshots"
	_ "github.com/basuotian/containerd/plugins/services/streaming"
	_ "github.com/basuotian/containerd/plugins/services/tasks"
	_ "github.com/basuotian/containerd/plugins/services/transfer"
	_ "github.com/basuotian/containerd/plugins/services/version"
	_ "github.com/basuotian/containerd/plugins/streaming"
	_ "github.com/basuotian/containerd/plugins/transfer"
)
