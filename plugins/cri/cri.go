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

package cri

import (
	"context"
	"fmt"
	"io"

	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	"google.golang.org/grpc"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"

	containerd "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/core/sandbox"
	criconfig "github.com/basuotian/containerd/internal/cri/config"
	"github.com/basuotian/containerd/internal/cri/constants"
	"github.com/basuotian/containerd/internal/cri/instrument"
	"github.com/basuotian/containerd/internal/cri/server"
	nriservice "github.com/basuotian/containerd/internal/nri"
	"github.com/basuotian/containerd/plugins"
	"github.com/basuotian/containerd/plugins/services/warning"
	"github.com/basuotian/containerd/version"
	"github.com/containerd/platforms"
)

// Register CRI service plugin
func init() {
	defaultConfig := criconfig.DefaultServerConfig()
	registry.Register(&plugin.Registration{
		Type: plugins.GRPCPlugin,
		ID:   "cri",
		Requires: []plugin.Type{
			plugins.CRIServicePlugin,
			plugins.PodSandboxPlugin,
			plugins.SandboxControllerPlugin,
			plugins.NRIApiPlugin,
			plugins.EventPlugin,
			plugins.ServicePlugin,
			plugins.LeasePlugin,
			plugins.SandboxStorePlugin,
			plugins.TransferPlugin,
			plugins.WarningPlugin,
		},
		Config:          &defaultConfig,
		ConfigMigration: configMigration,
		InitFn:          initCRIService,
	})
}

func initCRIService(ic *plugin.InitContext) (interface{}, error) {
	ctx := ic.Context
	config := ic.Config.(*criconfig.ServerConfig)

	// Get runtime service.
	criRuntimePlugin, err := ic.GetByID(plugins.CRIServicePlugin, "runtime")
	if err != nil {
		return nil, fmt.Errorf("unable to load CRI runtime service plugin dependency: %w", err)
	}

	// Get image service.
	criImagePlugin, err := ic.GetByID(plugins.CRIServicePlugin, "images")
	if err != nil {
		return nil, fmt.Errorf("unable to load CRI image service plugin dependency: %w", err)
	}

	if warnings, err := criconfig.ValidateServerConfig(ic.Context, config); err != nil {
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

	log.G(ctx).Info("Connect containerd service")
	client, err := containerd.New(
		"",
		containerd.WithDefaultNamespace(constants.K8sContainerdNamespace),
		containerd.WithDefaultPlatform(platforms.Default()),
		containerd.WithInMemoryServices(ic),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create containerd client: %w", err)
	}

	sbControllers, err := getSandboxControllers(ic)
	if err != nil {
		return nil, fmt.Errorf("failed to get sandbox controllers from plugins %v", err)
	}

	streamingConfig, err := config.StreamingConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get streaming config: %w", err)
	}

	options := &server.CRIServiceOptions{
		RuntimeService:     criRuntimePlugin.(server.RuntimeService),
		ImageService:       criImagePlugin.(server.ImageService),
		StreamingConfig:    streamingConfig,
		NRI:                getNRIAPI(ic),
		Client:             client,
		SandboxControllers: sbControllers,
	}
	is := criImagePlugin.(imageService).GRPCService()

	s, rs, err := server.NewCRIService(options)
	if err != nil {
		return nil, fmt.Errorf("failed to create CRI service: %w", err)
	}

	// RegisterReadiness() must be called after NewCRIService(): https://github.com/containerd/containerd/issues/9163
	ready := ic.RegisterReadiness()
	go func() {
		if err := s.Run(ready); err != nil {
			log.G(ctx).WithError(err).Fatal("Failed to run CRI service")
		}
		// TODO(random-liu): Whether and how we can stop containerd.
	}()

	service := &criGRPCServer{
		RuntimeServiceServer: rs,
		ImageServiceServer:   is,
		Closer:               s, // TODO: Where is close run?
		initializer:          s,
	}

	if config.DisableTCPService {
		return service, nil
	}

	return criGRPCServerWithTCP{service}, nil
}

type imageService interface {
	GRPCService() runtime.ImageServiceServer
}

type initializer interface {
	IsInitialized() bool
}

type criGRPCServer struct {
	runtime.RuntimeServiceServer
	runtime.ImageServiceServer
	io.Closer
	initializer
}

func (c *criGRPCServer) register(s *grpc.Server) error {
	instrumented := instrument.NewService(c)
	runtime.RegisterRuntimeServiceServer(s, instrumented)
	runtime.RegisterImageServiceServer(s, instrumented)
	return nil
}

// Register registers all required services onto a specific grpc server.
// This is used by containerd cri plugin.
func (c *criGRPCServer) Register(s *grpc.Server) error {
	return c.register(s)
}

type criGRPCServerWithTCP struct {
	*criGRPCServer
}

// RegisterTCP register all required services onto a GRPC server on TCP.
// This is used by containerd CRI plugin.
func (c criGRPCServerWithTCP) RegisterTCP(s *grpc.Server) error {
	return c.register(s)
}

// Get the NRI plugin, and set up our NRI API for it.
func getNRIAPI(ic *plugin.InitContext) nriservice.API {
	const (
		pluginType = plugins.NRIApiPlugin
		pluginName = "nri"
	)

	ctx := ic.Context

	p, err := ic.GetByID(pluginType, pluginName)
	if err != nil {
		log.G(ctx).Info("NRI service not found, NRI support disabled")
		return nil
	}

	api, ok := p.(nriservice.API)
	if !ok {
		log.G(ctx).Infof("NRI plugin (%s, %q) has incorrect type %T, NRI support disabled",
			pluginType, pluginName, api)
		return nil
	}

	log.G(ctx).Info("using experimental NRI integration - disable nri plugin to prevent this")
	return api
}

func getSandboxControllers(ic *plugin.InitContext) (map[string]sandbox.Controller, error) {
	sc := make(map[string]sandbox.Controller)
	sandboxers, err := ic.GetByType(plugins.SandboxControllerPlugin)
	if err != nil {
		return nil, err
	}
	for name, p := range sandboxers {
		sc[name] = p.(sandbox.Controller)
	}

	podSandboxers, err := ic.GetByType(plugins.PodSandboxPlugin)
	if err != nil {
		return nil, err
	}
	for name, p := range podSandboxers {
		sc[name] = p.(sandbox.Controller)
	}
	return sc, nil
}

func configMigration(ctx context.Context, configVersion int, pluginConfigs map[string]interface{}) error {
	if configVersion >= version.ConfigVersion {
		return nil
	}
	const pluginName = string(plugins.GRPCPlugin) + ".cri"
	src, ok := pluginConfigs[pluginName].(map[string]interface{})
	if !ok {
		return nil
	}

	dst := map[string]interface{}{}
	for _, k := range []string{
		"disable_tcp_service",
		"stream_server_address",
		"stream_server_port",
		"stream_idle_timeout",
		"enable_tls_streaming",
		"x509_key_pair_streaming",
	} {
		if val, ok := src[k]; ok {
			dst[k] = val
		}
	}

	pluginConfigs[pluginName] = dst
	return nil
}
