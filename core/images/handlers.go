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
	"errors"
	"fmt"
	"sort"

	"github.com/basuotian/containerd/core/content"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

var (
	// ErrSkipDesc is used to skip processing of a descriptor and
	// its descendants.
	ErrSkipDesc = errors.New("skip descriptor")

	// ErrStopHandler is used to signify that the descriptor
	// has been handled and should not be handled further.
	// This applies only to a single descriptor in a handler
	// chain and does not apply to descendant descriptors.
	ErrStopHandler = errors.New("stop handler")

	// ErrEmptyWalk is used when the WalkNotEmpty handlers return no
	// children (e.g.: they were filtered out).
	ErrEmptyWalk = errors.New("image might be filtered out")
)

// Handler handles image manifests
type Handler interface {
	Handle(ctx context.Context, desc ocispec.Descriptor) (subdescs []ocispec.Descriptor, err error)
}

// HandlerFunc function implementing the Handler interface
type HandlerFunc func(ctx context.Context, desc ocispec.Descriptor) (subdescs []ocispec.Descriptor, err error)

// Handle image manifests
func (fn HandlerFunc) Handle(ctx context.Context, desc ocispec.Descriptor) (subdescs []ocispec.Descriptor, err error) {
	return fn(ctx, desc)
}

// Handlers returns a handler that will run the handlers in sequence.
//
// A handler may return `ErrStopHandler` to stop calling additional handlers
func Handlers(handlers ...Handler) HandlerFunc {
	return func(ctx context.Context, desc ocispec.Descriptor) (subdescs []ocispec.Descriptor, err error) {
		var children []ocispec.Descriptor
		for _, handler := range handlers {
			ch, err := handler.Handle(ctx, desc)
			if err != nil {
				if errors.Is(err, ErrStopHandler) {
					break
				}
				return nil, err
			}

			children = append(children, ch...)
		}

		return children, nil
	}
}

// Walk the resources of an image and call the handler for each. If the handler
// decodes the sub-resources for each image,
//
// This differs from dispatch in that each sibling resource is considered
// synchronously.
func Walk(ctx context.Context, handler Handler, descs ...ocispec.Descriptor) error {
	for _, desc := range descs {

		children, err := handler.Handle(ctx, desc)
		if err != nil {
			if errors.Is(err, ErrSkipDesc) {
				continue // don't traverse the children.
			}
			return err
		}

		if len(children) > 0 {
			if err := Walk(ctx, handler, children...); err != nil {
				return err
			}
		}
	}
	return nil
}

// WalkNotEmpty works the same way Walk does, with the exception that it ensures that
// some children are still found by Walking the descriptors (for example, not all of
// them have been filtered out by one of the handlers). If there are no children,
// then an ErrEmptyWalk error is returned.
func WalkNotEmpty(ctx context.Context, handler Handler, descs ...ocispec.Descriptor) error {
	isEmpty := true
	var notEmptyHandler HandlerFunc = func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		children, err := handler.Handle(ctx, desc)
		if err != nil {
			return children, err
		}

		if len(children) > 0 {
			isEmpty = false
		}

		return children, nil
	}

	err := Walk(ctx, notEmptyHandler, descs...)
	if err != nil {
		return err
	}

	if isEmpty {
		return ErrEmptyWalk
	}

	return nil
}

// Dispatch runs the provided handler for content specified by the descriptors.
// If the handler decode subresources, they will be visited, as well.
//
// Handlers for siblings are run in parallel on the provided descriptors. A
// handler may return `ErrSkipDesc` to signal to the dispatcher to not traverse
// any children.
//
// A concurrency limiter can be passed in to limit the number of concurrent
// handlers running. When limiter is nil, there is no limit.
//
// Typically, this function will be used with `FetchHandler`, often composed
// with other handlers.
//
// If any handler returns an error, the dispatch session will be canceled.
func Dispatch(ctx context.Context, handler Handler, limiter *semaphore.Weighted, descs ...ocispec.Descriptor) error {
	eg, ctx2 := errgroup.WithContext(ctx)
	for _, desc := range descs {
		if limiter != nil {
			if err := limiter.Acquire(ctx, 1); err != nil {
				return err
			}
		}

		eg.Go(func() error {
			desc := desc

			children, err := handler.Handle(ctx2, desc)
			if limiter != nil {
				limiter.Release(1)
			}
			if err != nil {
				if errors.Is(err, ErrSkipDesc) {
					return nil // don't traverse the children.
				}
				return err
			}

			if len(children) > 0 {
				return Dispatch(ctx2, handler, limiter, children...)
			}

			return nil
		})
	}

	return eg.Wait()
}

// ChildrenHandler decodes well-known manifest types and returns their children.
//
// This is useful for supporting recursive fetch and other use cases where you
// want to do a full walk of resources.
//
// One can also replace this with another implementation to allow descending of
// arbitrary types.
func ChildrenHandler(provider content.Provider) HandlerFunc {
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		return Children(ctx, provider, desc)
	}
}

// SetChildrenLabels is a handler wrapper which sets labels for the content on
// the children returned by the handler and passes through the children.
// Must follow a handler that returns the children to be labeled.
func SetChildrenLabels(manager content.Manager, f HandlerFunc) HandlerFunc {
	return SetChildrenMappedLabels(manager, f, nil)
}

// SetChildrenMappedLabels is a handler wrapper which sets labels for the content on
// the children returned by the handler and passes through the children.
// Must follow a handler that returns the children to be labeled.
// The label map allows the caller to control the labels per child descriptor.
// For returned labels, the index of the child will be appended to the end
// except for the first index when the returned label does not end with '.'.
func SetChildrenMappedLabels(manager content.Manager, f HandlerFunc, labelMap func(ocispec.Descriptor) []string) HandlerFunc {
	if labelMap == nil {
		labelMap = ChildGCLabels
	}
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		children, err := f(ctx, desc)
		if err != nil {
			return children, err
		}

		if len(children) > 0 {
			var (
				info = content.Info{
					Digest: desc.Digest,
					Labels: map[string]string{},
				}
				fields = []string{}
				keys   = map[string]uint{}
			)
			for _, ch := range children {
				labelKeys := labelMap(ch)
				for _, key := range labelKeys {
					idx := keys[key]
					keys[key] = idx + 1
					if idx > 0 || key[len(key)-1] == '.' {
						key = fmt.Sprintf("%s%d", key, idx)
					}

					info.Labels[key] = ch.Digest.String()
					fields = append(fields, "labels."+key)
				}
			}

			_, err := manager.Update(ctx, info, fields...)
			if err != nil {
				return nil, err
			}
		}

		return children, err
	}
}

// FilterPlatforms is a handler wrapper which limits the descriptors returned
// based on matching the specified platform matcher.
func FilterPlatforms(f HandlerFunc, m platforms.Matcher) HandlerFunc {
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		children, err := f(ctx, desc)
		if err != nil {
			return children, err
		}

		var descs []ocispec.Descriptor

		if m == nil {
			descs = children
		} else {
			for _, d := range children {
				if d.Platform == nil || m.Match(*d.Platform) {
					descs = append(descs, d)
				}
			}
		}

		return descs, nil
	}
}

// LimitManifests is a handler wrapper which filters the manifest descriptors
// returned using the provided platform.
// The results will be ordered according to the comparison operator and
// use the ordering in the manifests for equal matches.
// A limit of 0 or less is considered no limit.
// A not found error is returned if no manifest is matched.
func LimitManifests(f HandlerFunc, m platforms.MatchComparer, n int) HandlerFunc {
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		children, err := f(ctx, desc)
		if err != nil {
			return children, err
		}

		// only limit manifests from an index
		if IsIndexType(desc.MediaType) {
			sort.SliceStable(children, func(i, j int) bool {
				if children[i].Platform == nil {
					return false
				}
				if children[j].Platform == nil {
					return true
				}
				return m.Less(*children[i].Platform, *children[j].Platform)
			})

			if n > 0 {
				if len(children) == 0 {
					return children, fmt.Errorf("no match for platform in manifest: %w", errdefs.ErrNotFound)
				}
				if len(children) > n {
					children = children[:n]
				}
			}
		}
		return children, nil
	}
}
