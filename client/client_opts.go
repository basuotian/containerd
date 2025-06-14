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
	"time"

	"github.com/basuotian/containerd/core/images"
	"github.com/basuotian/containerd/core/remotes"
	"github.com/basuotian/containerd/core/snapshots"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/semaphore"

	"google.golang.org/grpc"
)

type clientOpts struct {
	defaultns       string
	defaultRuntime  string
	defaultPlatform platforms.MatchComparer
	services        *services
	dialOptions     []grpc.DialOption
	extraDialOpts   []grpc.DialOption
	callOptions     []grpc.CallOption
	timeout         time.Duration
}

// Opt allows callers to set options on the containerd client
type Opt func(c *clientOpts) error

// WithDefaultNamespace sets the default namespace on the client
//
// Any operation that does not have a namespace set on the context will
// be provided the default namespace
func WithDefaultNamespace(ns string) Opt {
	return func(c *clientOpts) error {
		c.defaultns = ns
		return nil
	}
}

// WithDefaultRuntime sets the default runtime on the client
func WithDefaultRuntime(rt string) Opt {
	return func(c *clientOpts) error {
		c.defaultRuntime = rt
		return nil
	}
}

// WithDefaultPlatform sets the default platform matcher on the client
func WithDefaultPlatform(platform platforms.MatchComparer) Opt {
	return func(c *clientOpts) error {
		c.defaultPlatform = platform
		return nil
	}
}

// WithDialOpts allows grpc.DialOptions to be set on the connection
func WithDialOpts(opts []grpc.DialOption) Opt {
	return func(c *clientOpts) error {
		c.dialOptions = opts
		return nil
	}
}

// WithExtraDialOpts allows additional grpc.DialOptions to be set on the
// connection. Unlike [WithDialOpts], options set here are appended to,
// instead of overriding previous options, which allows setting options
// to extend containerd client's defaults.
//
// This option can be used multiple times to set additional dial options.
func WithExtraDialOpts(opts []grpc.DialOption) Opt {
	return func(c *clientOpts) error {
		c.extraDialOpts = append(c.extraDialOpts, opts...)
		return nil
	}
}

// WithCallOpts allows grpc.CallOptions to be set on the connection
func WithCallOpts(opts []grpc.CallOption) Opt {
	return func(c *clientOpts) error {
		c.callOptions = opts
		return nil
	}
}

// WithServices sets services used by the client.
func WithServices(opts ...ServicesOpt) Opt {
	return func(c *clientOpts) error {
		c.services = &services{}
		for _, o := range opts {
			o(c.services)
		}
		return nil
	}
}

// WithTimeout sets the connection timeout for the client
func WithTimeout(d time.Duration) Opt {
	return func(c *clientOpts) error {
		c.timeout = d
		return nil
	}
}

// RemoteOpt allows the caller to set distribution options for a remote
type RemoteOpt func(*Client, *RemoteContext) error

// WithPlatform allows the caller to specify a platform to retrieve
// content for
func WithPlatform(platform string) RemoteOpt {
	if platform == "" {
		platform = platforms.DefaultString()
	}
	return func(_ *Client, c *RemoteContext) error {
		for _, p := range c.Platforms {
			if p == platform {
				return nil
			}
		}

		c.Platforms = append(c.Platforms, platform)
		return nil
	}
}

// WithPlatformMatcher specifies the matcher to use for
// determining which platforms to pull content for.
// This value supersedes anything set with `WithPlatform`.
func WithPlatformMatcher(m platforms.MatchComparer) RemoteOpt {
	return func(_ *Client, c *RemoteContext) error {
		c.PlatformMatcher = m
		return nil
	}
}

// WithPullUnpack is used to unpack an image after pull. This
// uses the snapshotter, content store, and diff service
// configured for the client.
func WithPullUnpack(_ *Client, c *RemoteContext) error {
	c.Unpack = true
	return nil
}

// WithUnpackOpts is used to add unpack options to the unpacker.
func WithUnpackOpts(opts []UnpackOpt) RemoteOpt {
	return func(_ *Client, c *RemoteContext) error {
		c.UnpackOpts = append(c.UnpackOpts, opts...)
		return nil
	}
}

// WithPullSnapshotter specifies snapshotter name used for unpacking.
func WithPullSnapshotter(snapshotterName string, opts ...snapshots.Opt) RemoteOpt {
	return func(_ *Client, c *RemoteContext) error {
		c.Snapshotter = snapshotterName
		c.SnapshotterOpts = opts
		return nil
	}
}

// WithPullLabel sets a label to be associated with a pulled reference
func WithPullLabel(key, value string) RemoteOpt {
	return func(_ *Client, rc *RemoteContext) error {
		if rc.Labels == nil {
			rc.Labels = make(map[string]string)
		}

		rc.Labels[key] = value
		return nil
	}
}

// WithPullLabels associates a set of labels to a pulled reference
func WithPullLabels(labels map[string]string) RemoteOpt {
	return func(_ *Client, rc *RemoteContext) error {
		if rc.Labels == nil {
			rc.Labels = make(map[string]string)
		}

		for k, v := range labels {
			rc.Labels[k] = v
		}
		return nil
	}
}

// WithChildLabelMap sets the map function used to define the labels set
// on referenced child content in the content store. This can be used
// to overwrite the default GC labels or filter which labels get set
// for content.
// The default is `images.ChildGCLabels`.
func WithChildLabelMap(fn func(ocispec.Descriptor) []string) RemoteOpt {
	return func(_ *Client, c *RemoteContext) error {
		c.ChildLabelMap = fn
		return nil
	}
}

// WithResolver specifies the resolver to use.
func WithResolver(resolver remotes.Resolver) RemoteOpt {
	return func(client *Client, c *RemoteContext) error {
		c.Resolver = resolver
		return nil
	}
}

// WithImageHandler adds a base handler to be called on dispatch.
func WithImageHandler(h images.Handler) RemoteOpt {
	return func(client *Client, c *RemoteContext) error {
		c.BaseHandlers = append(c.BaseHandlers, h)
		return nil
	}
}

// WithImageHandlerWrapper wraps the handlers to be called on dispatch.
func WithImageHandlerWrapper(w func(images.Handler) images.Handler) RemoteOpt {
	return func(client *Client, c *RemoteContext) error {
		c.HandlerWrapper = w
		return nil
	}
}

// WithDownloadLimiter sets the limiter for concurrent download operations.
func WithDownloadLimiter(limiter *semaphore.Weighted) RemoteOpt {
	return func(client *Client, c *RemoteContext) error {
		c.DownloadLimiter = limiter
		return nil
	}
}

// WithMaxConcurrentDownloads sets max concurrent download limit.
func WithMaxConcurrentDownloads(max int) RemoteOpt {
	return func(client *Client, c *RemoteContext) error {
		c.MaxConcurrentDownloads = max
		return nil
	}
}

// WithConcurrentLayerFetchBuffer sets the buffer size for concurrent layer fetches.
func WithConcurrentLayerFetchBuffer(buffer int) RemoteOpt {
	return func(client *Client, c *RemoteContext) error {
		c.ConcurrentLayerFetchBuffer = buffer
		return nil
	}
}

// WithMaxConcurrentUploadedLayers sets max concurrent uploaded layer limit.
func WithMaxConcurrentUploadedLayers(max int) RemoteOpt {
	return func(client *Client, c *RemoteContext) error {
		c.MaxConcurrentUploadedLayers = max
		return nil
	}
}

// WithAllMetadata downloads all manifests and known-configuration files
func WithAllMetadata() RemoteOpt {
	return func(_ *Client, c *RemoteContext) error {
		c.AllMetadata = true
		return nil
	}
}
