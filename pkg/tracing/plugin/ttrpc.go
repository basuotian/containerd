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

package plugin

import (
	"github.com/basuotian/containerd/plugins"
	"github.com/containerd/otelttrpc"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	"github.com/containerd/ttrpc"
)

func init() {
	const pluginName = "otelttrpc"

	registry.Register(&plugin.Registration{
		ID:   pluginName,
		Type: plugins.TTRPCPlugin,
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			return otelttrpcopts{}, nil
		},
	})
}

type otelttrpcopts struct{}

func (otelttrpcopts) UnaryServerInterceptor() ttrpc.UnaryServerInterceptor {
	return otelttrpc.UnaryServerInterceptor()
}

func (otelttrpcopts) UnaryClientInterceptor() ttrpc.UnaryClientInterceptor {
	return otelttrpc.UnaryClientInterceptor()
}
