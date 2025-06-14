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

package docker

import (
	"context"
	"testing"

	fuzz "github.com/AdaLogics/go-fuzz-headers"
	"github.com/basuotian/containerd/plugins/content/local"
	"github.com/containerd/log"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func FuzzConvertManifest(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()

		// Do not log the message below
		// level=warning msg="do nothing for media type: ..."
		log.G(ctx).Logger.SetLevel(log.PanicLevel)

		f := fuzz.NewConsumer(data)
		desc := ocispec.Descriptor{}
		err := f.GenerateStruct(&desc)
		if err != nil {
			return
		}
		tmpdir := t.TempDir()
		cs, err := local.NewStore(tmpdir)
		if err != nil {
			return
		}
		_, _ = ConvertManifest(ctx, cs, desc)
	})
}
