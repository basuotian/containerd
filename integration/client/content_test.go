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
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	. "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/core/content"
	"github.com/basuotian/containerd/core/content/testsuite"
	"github.com/basuotian/containerd/pkg/namespaces"
	"github.com/containerd/errdefs"
)

func newContentStore(ctx context.Context, root string) (context.Context, content.Store, func() error, error) {
	client, err := New(address)
	if err != nil {
		return nil, nil, nil, err
	}

	var (
		count atomic.Uint64
		cs    = client.ContentStore()
		name  = testsuite.Name(ctx)
	)

	wrap := func(ctx context.Context, sharedNS bool) (context.Context, func(context.Context) error, error) {
		n := count.Add(1)
		ctx = namespaces.WithNamespace(ctx, fmt.Sprintf("%s-n%d", name, n))
		return client.WithLease(ctx)
	}

	ctx = testsuite.SetContextWrapper(ctx, wrap)

	return ctx, cs, func() error {
		for i := uint64(1); i <= count.Load(); i++ {
			ctx = namespaces.WithNamespace(ctx, fmt.Sprintf("%s-n%d", name, i))
			statuses, err := cs.ListStatuses(ctx)
			if err != nil {
				return err
			}
			for _, st := range statuses {
				if err := cs.Abort(ctx, st.Ref); err != nil && !errdefs.IsNotFound(err) {
					return fmt.Errorf("failed to abort %s: %w", st.Ref, err)
				}
			}
			err = cs.Walk(ctx, func(info content.Info) error {
				if err := cs.Delete(ctx, info.Digest); err != nil {
					if errdefs.IsNotFound(err) {
						return nil
					}

					return err
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil

	}, nil
}

func TestContentClient(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	testsuite.ContentSuite(t, "ContentClient", newContentStore)
}
