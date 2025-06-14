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
	"errors"
	"fmt"

	"github.com/basuotian/containerd/cmd/ctr/commands"
	"github.com/basuotian/containerd/core/images/converter"
	"github.com/basuotian/containerd/core/images/converter/uncompress"
	"github.com/containerd/platforms"
	"github.com/urfave/cli/v2"
)

var convertCommand = &cli.Command{
	Name:      "convert",
	Usage:     "Convert an image",
	ArgsUsage: "[flags] <source_ref> <target_ref>",
	Description: `Convert an image format.

e.g., 'ctr convert --uncompress --oci example.com/foo:orig example.com/foo:converted'

Use '--platform' to define the output platform.
When '--all-platforms' is given all images in a manifest list must be available.
`,
	Flags: []cli.Flag{
		// generic flags
		&cli.BoolFlag{
			Name:  "uncompress",
			Usage: "Convert tar.gz layers to uncompressed tar layers",
		},
		&cli.BoolFlag{
			Name:  "oci",
			Usage: "Convert Docker media types to OCI media types",
		},
		// platform flags
		&cli.StringSliceFlag{
			Name:  "platform",
			Usage: "Pull content from a specific platform",
			Value: cli.NewStringSlice(),
		},
		&cli.BoolFlag{
			Name:  "all-platforms",
			Usage: "Exports content from all platforms",
		},
	},
	Action: func(cliContext *cli.Context) error {
		var convertOpts []converter.Opt
		srcRef := cliContext.Args().Get(0)
		targetRef := cliContext.Args().Get(1)
		if srcRef == "" || targetRef == "" {
			return errors.New("src and target image need to be specified")
		}

		if !cliContext.Bool("all-platforms") {
			if pss := cliContext.StringSlice("platform"); len(pss) > 0 {
				all, err := platforms.ParseAll(pss)
				if err != nil {
					return err
				}
				convertOpts = append(convertOpts, converter.WithPlatform(platforms.Ordered(all...)))
			} else {
				convertOpts = append(convertOpts, converter.WithPlatform(platforms.DefaultStrict()))
			}
		}

		if cliContext.Bool("uncompress") {
			convertOpts = append(convertOpts, converter.WithLayerConvertFunc(uncompress.LayerConvertFunc))
		}

		if cliContext.Bool("oci") {
			convertOpts = append(convertOpts, converter.WithDockerToOCI(true))
		}

		client, ctx, cancel, err := commands.NewClient(cliContext)
		if err != nil {
			return err
		}
		defer cancel()

		newImg, err := converter.Convert(ctx, client, targetRef, srcRef, convertOpts...)
		if err != nil {
			return err
		}
		fmt.Fprintln(cliContext.App.Writer, newImg.Target.Digest.String())
		return nil
	},
}
