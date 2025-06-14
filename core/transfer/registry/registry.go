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

package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/basuotian/containerd/core/remotes"
	"github.com/basuotian/containerd/core/remotes/docker"
	"github.com/basuotian/containerd/core/remotes/docker/config"
	"github.com/basuotian/containerd/core/streaming"
	"github.com/basuotian/containerd/core/transfer"
	"github.com/basuotian/containerd/core/transfer/plugins"
	tstreaming "github.com/basuotian/containerd/core/transfer/streaming"
	"github.com/basuotian/containerd/pkg/httpdbg"
	transfertypes "github.com/containerd/containerd/api/types/transfer"
	"github.com/containerd/log"
	"github.com/containerd/typeurl/v2"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func init() {
	// TODO: Move this to separate package?
	plugins.Register(&transfertypes.OCIRegistry{}, &OCIRegistry{})
}

type registryOpts struct {
	headers       http.Header
	creds         CredentialHelper
	hostDir       string
	defaultScheme string
	httpDebug     bool
	httpTrace     bool
	localStream   io.WriteCloser
}

// Opt sets registry-related configurations.
type Opt func(o *registryOpts) error

// WithHeaders configures HTTP request header fields sent by the resolver.
func WithHeaders(headers http.Header) Opt {
	return func(o *registryOpts) error {
		o.headers = headers
		return nil
	}
}

// WithCredentials configures a helper that provides credentials for a host.
func WithCredentials(creds CredentialHelper) Opt {
	return func(o *registryOpts) error {
		o.creds = creds
		return nil
	}
}

// WithHostDir specifies the host configuration directory.
func WithHostDir(hostDir string) Opt {
	return func(o *registryOpts) error {
		o.hostDir = hostDir
		return nil
	}
}

// WithDefaultScheme specifies the default scheme for registry configuration
func WithDefaultScheme(s string) Opt {
	return func(o *registryOpts) error {
		o.defaultScheme = s
		return nil
	}
}

// WithHTTPDebug dumps requests made to an OCI registry. Useful to debug interactions between containerd and registry.
func WithHTTPDebug() Opt {
	return func(o *registryOpts) error {
		o.httpDebug = true
		return nil
	}
}

// WithHTTPTrace traces HTTP events made to an OCI registry.
func WithHTTPTrace() Opt {
	return func(o *registryOpts) error {
		o.httpTrace = true
		return nil
	}
}

// WithClientStream tells the registry to stream HTTP debug data back to the client.
// Applicable only when HTTP debug or tracing enabled.
func WithClientStream(writer io.WriteCloser) Opt {
	return func(o *registryOpts) error {
		o.localStream = writer
		return nil
	}
}

// NewOCIRegistry initializes with hosts, authorizer callback, and headers
func NewOCIRegistry(ctx context.Context, ref string, opts ...Opt) (*OCIRegistry, error) {
	var ropts registryOpts
	for _, o := range opts {
		if err := o(&ropts); err != nil {
			return nil, err
		}
	}

	hostOptions := config.HostOptions{}
	if ropts.hostDir != "" {
		hostOptions.HostDir = config.HostDirFromRoot(ropts.hostDir)
	}
	if ropts.creds != nil {
		// TODO: Support bearer
		hostOptions.Credentials = func(host string) (string, string, error) {
			c, err := ropts.creds.GetCredentials(context.Background(), ref, host)
			if err != nil {
				return "", "", err
			}

			return c.Username, c.Secret, nil
		}
	}
	if ropts.defaultScheme != "" {
		hostOptions.DefaultScheme = ropts.defaultScheme
	}

	hostOptions.UpdateClient = func(client *http.Client) error {
		if ropts.httpDebug {
			httpdbg.DumpRequests(ctx, client, ropts.localStream)
		}
		if ropts.httpTrace {
			httpdbg.DumpTraces(ctx, client)
		}
		return nil
	}

	resolver := docker.NewResolver(docker.ResolverOptions{
		Hosts:   config.ConfigureHosts(ctx, hostOptions),
		Headers: ropts.headers,
	})

	return &OCIRegistry{
		reference:     ref,
		headers:       ropts.headers,
		creds:         ropts.creds,
		resolver:      resolver,
		hostDir:       ropts.hostDir,
		defaultScheme: ropts.defaultScheme,
		httpDebug:     ropts.httpDebug,
		httpTrace:     ropts.httpTrace,
		localStream:   ropts.localStream,
	}, nil
}

// From stream
type CredentialHelper interface {
	GetCredentials(ctx context.Context, ref, host string) (Credentials, error)
}

type Credentials struct {
	Host     string
	Username string
	Secret   string
	Header   string
}

// OCI
type OCIRegistry struct {
	reference string

	headers http.Header
	creds   CredentialHelper

	resolver remotes.Resolver

	hostDir string

	defaultScheme string

	httpDebug   bool
	httpTrace   bool
	localStream io.WriteCloser

	// This could be an interface which returns resolver?
	// Resolver could also be a plug-able interface, to call out to a program to fetch?
}

func (r *OCIRegistry) String() string {
	return fmt.Sprintf("OCI Registry (%s)", r.reference)
}

func (r *OCIRegistry) Image() string {
	return r.reference
}

func (r *OCIRegistry) Resolve(ctx context.Context) (name string, desc ocispec.Descriptor, err error) {
	return r.resolver.Resolve(ctx, r.reference)
}

func (r *OCIRegistry) SetResolverOptions(options ...transfer.ImageResolverOption) {
	if resolver, ok := r.resolver.(remotes.ResolverWithOptions); ok {
		resolver.SetOptions(options...)
	}
}

func (r *OCIRegistry) Fetcher(ctx context.Context, ref string) (transfer.Fetcher, error) {
	return r.resolver.Fetcher(ctx, ref)
}

func (r *OCIRegistry) Pusher(ctx context.Context, desc ocispec.Descriptor) (transfer.Pusher, error) {
	var ref = r.reference
	// Annotate ref with digest to push only push tag for single digest
	if !strings.Contains(ref, "@") {
		ref = ref + "@" + desc.Digest.String()
	}
	return r.resolver.Pusher(ctx, ref)
}

func (r *OCIRegistry) MarshalAny(ctx context.Context, sm streaming.StreamCreator) (typeurl.Any, error) {
	res := &transfertypes.RegistryResolver{}
	if r.headers != nil {
		res.Headers = map[string]string{}
		for k := range r.headers {
			res.Headers[k] = r.headers.Get(k)
		}
	}
	if r.creds != nil {
		sid := tstreaming.GenerateID("creds")
		stream, err := sm.Create(ctx, sid)
		if err != nil {
			return nil, err
		}
		go func() {
			// Check for context cancellation as well
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				req, err := stream.Recv()
				if err != nil {
					// If not EOF, log error
					return
				}

				var s transfertypes.AuthRequest
				if err := typeurl.UnmarshalTo(req, &s); err != nil {
					log.G(ctx).WithError(err).Error("failed to unmarshal credential request")
					continue
				}
				creds, err := r.creds.GetCredentials(ctx, s.Reference, s.Host)
				if err != nil {
					log.G(ctx).WithError(err).Error("failed to get credentials")
					continue
				}
				var resp transfertypes.AuthResponse
				if creds.Header != "" {
					resp.AuthType = transfertypes.AuthType_HEADER
					resp.Secret = creds.Header
				} else if creds.Username != "" {
					resp.AuthType = transfertypes.AuthType_CREDENTIALS
					resp.Username = creds.Username
					resp.Secret = creds.Secret
				} else {
					resp.AuthType = transfertypes.AuthType_REFRESH
					resp.Secret = creds.Secret
				}

				a, err := typeurl.MarshalAny(&resp)
				if err != nil {
					log.G(ctx).WithError(err).Error("failed to marshal credential response")
					continue
				}

				if err := stream.Send(a); err != nil {
					if !errors.Is(err, io.EOF) {
						log.G(ctx).WithError(err).Error("unexpected send failure")
					}
					return
				}
			}

		}()
		res.AuthStream = sid
	}

	if r.httpDebug || r.httpTrace {
		switch {
		case r.httpDebug && r.httpTrace:
			res.HttpDebug = transfertypes.HTTPDebug_BOTH
		case r.httpDebug:
			res.HttpDebug = transfertypes.HTTPDebug_DEBUG
		case r.httpTrace:
			res.HttpDebug = transfertypes.HTTPDebug_TRACE
		default:
			res.HttpDebug = transfertypes.HTTPDebug_DISABLED
		}

		if r.localStream != nil {
			res.LogsStream = tstreaming.GenerateID("http-debug-logs")

			stream, err := sm.Create(ctx, res.LogsStream)
			if err != nil {
				return nil, fmt.Errorf("failed to create stream for HTTP debug logs: %w", err)
			}

			go func() {
				// Start pumping logs to the client
				_, err := io.Copy(r.localStream, tstreaming.ReceiveStream(ctx, stream))
				if err != nil && !errors.Is(err, io.EOF) {
					log.G(ctx).WithError(err).Error("failed to copy HTTP debug logs stream")
				}

				if err := r.localStream.Close(); err != nil {
					log.G(ctx).WithError(err).Error("failed to close HTTP debug logs local stream")
				}
			}()
		}
	}

	res.HostDir = r.hostDir
	res.DefaultScheme = r.defaultScheme
	s := &transfertypes.OCIRegistry{
		Reference: r.reference,
		Resolver:  res,
	}

	return typeurl.MarshalAny(s)
}

func (r *OCIRegistry) UnmarshalAny(ctx context.Context, sm streaming.StreamGetter, a typeurl.Any) error {
	var s transfertypes.OCIRegistry
	if err := typeurl.UnmarshalTo(a, &s); err != nil {
		return err
	}

	hostOptions := config.HostOptions{}
	if s.Resolver != nil {
		if s.Resolver.HostDir != "" {
			hostOptions.HostDir = config.HostDirFromRoot(s.Resolver.HostDir)
		}
		if s.Resolver.DefaultScheme != "" {
			hostOptions.DefaultScheme = s.Resolver.DefaultScheme
		}
		if sid := s.Resolver.AuthStream; sid != "" {
			stream, err := sm.Get(ctx, sid)
			if err != nil {
				log.G(ctx).WithError(err).WithField("stream", sid).Debug("failed to get auth stream")
				return err
			}
			r.creds = &credCallback{
				stream: stream,
			}
			hostOptions.Credentials = func(host string) (string, string, error) {
				c, err := r.creds.GetCredentials(context.Background(), s.Reference, host)
				if err != nil {
					return "", "", err
				}

				return c.Username, c.Secret, nil
			}
		}
		r.headers = http.Header{}
		for k, v := range s.Resolver.Headers {
			r.headers.Add(k, v)
		}

		if s.Resolver.HttpDebug != transfertypes.HTTPDebug_DISABLED {
			var writer io.WriteCloser

			// Stream to local client.
			if sid := s.Resolver.LogsStream; sid != "" {
				stream, err := sm.Get(ctx, sid)
				if err != nil {
					return fmt.Errorf("failed to get stream for HTTP debug logs: %w", err)
				}

				writer = tstreaming.WriteByteStream(ctx, stream)
			} else {
				writer = log.G(ctx).Writer()
			}

			go func() {
				<-ctx.Done()
				if err := writer.Close(); err != nil {
					log.G(ctx).Errorf("failed to close HTTP debug logs stream: %v", err)
				}
			}()

			hostOptions.UpdateClient = func(client *http.Client) error {
				switch s.Resolver.HttpDebug {
				case transfertypes.HTTPDebug_DEBUG:
					httpdbg.DumpRequests(ctx, client, writer)
				case transfertypes.HTTPDebug_TRACE:
					httpdbg.DumpTraces(ctx, client)
				case transfertypes.HTTPDebug_BOTH:
					httpdbg.DumpRequests(ctx, client, writer)
					httpdbg.DumpTraces(ctx, client)
				}
				return nil
			}
		}
	}

	r.reference = s.Reference
	r.resolver = docker.NewResolver(docker.ResolverOptions{
		Hosts:   config.ConfigureHosts(ctx, hostOptions),
		Headers: r.headers,
	})

	return nil
}

type credCallback struct {
	sync.Mutex
	stream streaming.Stream
}

func (cc *credCallback) GetCredentials(ctx context.Context, ref, host string) (Credentials, error) {
	cc.Lock()
	defer cc.Unlock()

	ar := &transfertypes.AuthRequest{
		Host:      host,
		Reference: ref,
	}
	anyType, err := typeurl.MarshalAny(ar)
	if err != nil {
		return Credentials{}, err
	}
	if err := cc.stream.Send(anyType); err != nil {
		return Credentials{}, err
	}
	resp, err := cc.stream.Recv()
	if err != nil {
		return Credentials{}, err
	}
	var s transfertypes.AuthResponse
	if err := typeurl.UnmarshalTo(resp, &s); err != nil {
		return Credentials{}, err
	}
	creds := Credentials{
		Host: host,
	}
	switch s.AuthType {
	case transfertypes.AuthType_CREDENTIALS:
		creds.Username = s.Username
		creds.Secret = s.Secret
	case transfertypes.AuthType_REFRESH:
		creds.Secret = s.Secret
	case transfertypes.AuthType_HEADER:
		creds.Header = s.Secret
	}

	return creds, nil
}
