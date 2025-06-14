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
	"fmt"
	"os/exec"

	"github.com/basuotian/containerd/core/metadata"
	"github.com/basuotian/containerd/plugins"
	"github.com/basuotian/containerd/plugins/diff/erofs"
	"github.com/containerd/platforms"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
)

// Config represents configuration for the erofs plugin.
type Config struct {
	// MkfsOptions are extra options used for the applier
	MkfsOptions []string `toml:"mkfs_options"`
}

func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.DiffPlugin,
		ID:   "erofs",
		Requires: []plugin.Type{
			plugins.MetadataPlugin,
		},
		Config: &Config{},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			_, err := exec.LookPath("mkfs.erofs")
			if err != nil {
				return nil, fmt.Errorf("could not find mkfs.erofs: %v: %w", err, plugin.ErrSkipPlugin)
			}

			md, err := ic.GetSingle(plugins.MetadataPlugin)
			if err != nil {
				return nil, err
			}

			ic.Meta.Platforms = append(ic.Meta.Platforms, platforms.DefaultSpec())
			cs := md.(*metadata.DB).ContentStore()
			config := ic.Config.(*Config)

			return erofs.NewErofsDiffer(cs, config.MkfsOptions), nil
		},
	})
}
