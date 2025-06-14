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

package images

import (
	"context"
	"fmt"
	"path/filepath"

	containerd "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/core/metadata"
	"github.com/basuotian/containerd/core/snapshots"
	"github.com/basuotian/containerd/core/transfer"
	criconfig "github.com/basuotian/containerd/internal/cri/config"
	"github.com/basuotian/containerd/internal/cri/constants"
	"github.com/basuotian/containerd/internal/cri/server/images"
	"github.com/basuotian/containerd/plugins"
	"github.com/basuotian/containerd/plugins/services/warning"
	"github.com/basuotian/containerd/version"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
)

func init() {
	config := criconfig.DefaultImageConfig()

	registry.Register(&plugin.Registration{
		Type:   plugins.CRIServicePlugin,
		ID:     "images",
		Config: &config,
		Requires: []plugin.Type{
			plugins.LeasePlugin,
			plugins.MetadataPlugin,
			plugins.SandboxStorePlugin,
			plugins.ServicePlugin,  // For client
			plugins.SnapshotPlugin, // For root directory properties
			plugins.TransferPlugin, // For pulling image using transfer service
			plugins.WarningPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			m, err := ic.GetSingle(plugins.MetadataPlugin)
			if err != nil {
				return nil, err
			}
			mdb := m.(*metadata.DB)

			if warnings, err := criconfig.ValidateImageConfig(ic.Context, &config); err != nil {
				return nil, fmt.Errorf("invalid cri image config: %w", err)
			} else if len(warnings) > 0 {
				ws, err := ic.GetSingle(plugins.WarningPlugin)
				if err != nil {
					return nil, err
				}
				warn := ws.(warning.Service)
				for _, w := range warnings {
					warn.Emit(ic.Context, w)
				}
			}

			if !config.UseLocalImagePull {
				criconfig.CheckLocalImagePullConfigs(ic.Context, &config)
			}

			ts, err := ic.GetSingle(plugins.TransferPlugin)
			if err != nil {
				return nil, err
			}

			options := &images.CRIImageServiceOptions{
				Content:          mdb.ContentStore(),
				RuntimePlatforms: map[string]images.ImagePlatform{},
				Snapshotters:     map[string]snapshots.Snapshotter{},
				ImageFSPaths:     map[string]string{},
				Transferrer:      ts.(transfer.Transferrer),
			}

			ctrdCli, err := containerd.New(
				"",
				containerd.WithDefaultNamespace(constants.K8sContainerdNamespace),
				containerd.WithDefaultPlatform(platforms.Default()),
				containerd.WithInMemoryServices(ic),
			)
			if err != nil {
				return nil, fmt.Errorf("unable to init client for cri image service: %w", err)
			}
			options.Images = ctrdCli.ImageService()
			options.Client = ctrdCli

			allSnapshotters := mdb.Snapshotters()
			defaultSnapshotter := config.Snapshotter
			if s, ok := allSnapshotters[defaultSnapshotter]; ok {
				options.Snapshotters[defaultSnapshotter] = s
			} else {
				return nil, fmt.Errorf("failed to find snapshotter %q", defaultSnapshotter)
			}

			snapshotRoot := func(snapshotter string) (snapshotRoot string) {
				if plugin := ic.Plugins().Get(plugins.SnapshotPlugin, snapshotter); plugin != nil {
					snapshotRoot = plugin.Meta.Exports["root"]
				}
				if snapshotRoot == "" {
					// Try a root in the same parent as this plugin
					snapshotRoot = filepath.Join(filepath.Dir(ic.Properties[plugins.PropertyRootDir]), plugins.SnapshotPlugin.String()+"."+snapshotter)
				}
				return snapshotRoot
			}

			options.ImageFSPaths[defaultSnapshotter] = snapshotRoot(defaultSnapshotter)
			log.L.Infof("Get image filesystem path %q for snapshotter %q", options.ImageFSPaths[defaultSnapshotter], defaultSnapshotter)

			for runtimeName, rp := range config.RuntimePlatforms {
				snapshotter := rp.Snapshotter
				if snapshotter == "" {
					snapshotter = defaultSnapshotter
				}

				if _, ok := options.ImageFSPaths[snapshotter]; !ok {
					options.ImageFSPaths[snapshotter] = snapshotRoot(snapshotter)
					log.L.Infof("Get image filesystem path %q for snapshotter %q", options.ImageFSPaths[snapshotter], snapshotter)
				}

				platform := platforms.DefaultSpec()
				if rp.Platform != "" {
					p, err := platforms.Parse(rp.Platform)
					if err != nil {
						return nil, fmt.Errorf("unable to parse platform %q: %w", rp.Platform, err)
					}
					platform = p
				}

				options.RuntimePlatforms[runtimeName] = images.ImagePlatform{
					Snapshotter: snapshotter,
					Platform:    platform,
				}
			}

			service, err := images.NewService(config, options)
			if err != nil {
				return nil, fmt.Errorf("failed to create image service: %w", err)
			}

			return service, nil
		},
		ConfigMigration: configMigration,
	})
}

func configMigration(ctx context.Context, configVersion int, pluginConfigs map[string]interface{}) error {
	if configVersion >= version.ConfigVersion {
		return nil
	}
	original, ok := pluginConfigs[string(plugins.GRPCPlugin)+".cri"]
	if !ok {
		return nil
	}
	src := original.(map[string]interface{})
	updated, ok := pluginConfigs[string(plugins.CRIServicePlugin)+".images"]
	var dst map[string]interface{}
	if ok {
		dst = updated.(map[string]interface{})
	} else {
		dst = map[string]interface{}{}
	}

	migrateConfig(dst, src)
	pluginConfigs[string(plugins.CRIServicePlugin)+".images"] = dst
	return nil
}
func migrateConfig(dst, src map[string]interface{}) {
	var pinnedImages map[string]interface{}
	if v, ok := dst["pinned_images"]; ok {
		pinnedImages = v.(map[string]interface{})
	} else {
		pinnedImages = map[string]interface{}{}
	}

	if simage, ok := src["sandbox_image"]; ok {
		pinnedImages["sandbox"] = simage
	}
	if len(pinnedImages) > 0 {
		dst["pinned_images"] = pinnedImages
	}

	for _, key := range []string{
		"registry",
		"image_decryption",
		"max_concurrent_downloads",
		"image_pull_progress_timeout",
		"image_pull_with_sync_fs",
		"stats_collect_period",
	} {
		if val, ok := src[key]; ok {
			dst[key] = val
		}
	}

	containerdConf, ok := src["containerd"]
	if !ok {
		return
	}
	containerdConfMap := containerdConf.(map[string]interface{})
	runtimesConf, ok := containerdConfMap["runtimes"]
	if !ok {
		return
	}

	var runtimePlatforms map[string]interface{}
	if v, ok := dst["runtime_platform"]; ok {
		runtimePlatforms = v.(map[string]interface{})
	} else {
		runtimePlatforms = map[string]interface{}{}
	}
	for runtime, v := range runtimesConf.(map[string]interface{}) {
		runtimeConf := v.(map[string]interface{})
		if snapshotter, ok := runtimeConf["snapshot"]; ok && snapshotter != "" {
			runtimePlatforms[runtime] = map[string]interface{}{
				"platform":    platforms.DefaultStrict(),
				"snapshotter": snapshotter,
			}
		}
	}
	if len(runtimePlatforms) > 0 {
		dst["runtime_platform"] = runtimePlatforms
	}

	for _, key := range []string{
		"snapshotter",
		"disable_snapshot_annotations",
		"discard_unpacked_layers",
	} {
		if val, ok := containerdConfMap[key]; ok {
			dst[key] = val
		}
	}
}
