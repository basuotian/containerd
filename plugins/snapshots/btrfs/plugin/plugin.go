//go:build linux && !no_btrfs && cgo

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
	"errors"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/basuotian/containerd/plugins"
	"github.com/basuotian/containerd/plugins/snapshots/btrfs"
	"github.com/containerd/platforms"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
)

// Config represents configuration for the btrfs plugin.
type Config struct {
	// Root directory for the plugin
	RootPath string `toml:"root_path"`
}

func init() {
	registry.Register(&plugin.Registration{
		ID:     "btrfs",
		Type:   plugins.SnapshotPlugin,
		Config: &Config{},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			ic.Meta.Platforms = []ocispec.Platform{platforms.DefaultSpec()}

			config, ok := ic.Config.(*Config)
			if !ok {
				return nil, errors.New("invalid btrfs configuration")
			}

			root := ic.Properties[plugins.PropertyRootDir]
			if len(config.RootPath) != 0 {
				root = config.RootPath
			}

			ic.Meta.Exports[plugins.SnapshotterRootDir] = root
			return btrfs.NewSnapshotter(root)
		},
	})
}
