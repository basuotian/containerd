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
	"io"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/basuotian/containerd/cmd/ctr/commands"
	"github.com/basuotian/containerd/core/images/archive"
	"github.com/basuotian/containerd/core/transfer"
	tarchive "github.com/basuotian/containerd/core/transfer/archive"
	"github.com/basuotian/containerd/core/transfer/image"
	"github.com/containerd/platforms"
)

var exportCommand = &cli.Command{
	Name:      "export",
	Usage:     "Export images",
	ArgsUsage: "[flags] <out> <image> ...",
	Description: `Export images to an OCI tar archive.

Tar output is formatted as an OCI archive, a Docker manifest is provided for the platform.
Use '--skip-manifest-json' to avoid including the Docker manifest.json file.
Use '--platform' to define the output platform.
When '--all-platforms' is given all images in a manifest list must be available.
`,
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "skip-manifest-json",
			Usage: "Do not add Docker compatible manifest.json to archive",
		},
		&cli.BoolFlag{
			Name:  "skip-non-distributable",
			Usage: "Do not add non-distributable blobs such as Windows layers to archive",
		},
		&cli.StringSliceFlag{
			Name:  "platform",
			Usage: "Pull content from a specific platform",
			Value: cli.NewStringSlice(),
		},
		&cli.BoolFlag{
			Name:  "all-platforms",
			Usage: "Exports content from all platforms",
		},
		&cli.BoolFlag{
			Name:  "local",
			Usage: "Run export locally rather than through transfer API",
		},
	},
	Action: func(cliContext *cli.Context) error {
		var (
			out        = cliContext.Args().First()
			images     = cliContext.Args().Tail()
			exportOpts = []archive.ExportOpt{}
		)
		if out == "" || len(images) == 0 {
			return errors.New("please provide both an output filename and an image reference to export")
		}

		client, ctx, cancel, err := commands.NewClient(cliContext)
		if err != nil {
			return err
		}
		defer cancel()

		var w io.WriteCloser
		if out == "-" {
			w = os.Stdout
		} else {
			w, err = os.Create(out)
			if err != nil {
				return err
			}
		}
		defer w.Close()

		if !cliContext.Bool("local") {
			pf, done := ProgressHandler(ctx, os.Stdout)
			defer done()

			exportOpts := []tarchive.ExportOpt{}
			if pss := cliContext.StringSlice("platform"); len(pss) > 0 {
				for _, ps := range pss {
					p, err := platforms.Parse(ps)
					if err != nil {
						return fmt.Errorf("invalid platform %q: %w", ps, err)
					}
					exportOpts = append(exportOpts, tarchive.WithPlatform(p))
				}
			}
			if cliContext.Bool("all-platforms") {
				exportOpts = append(exportOpts, tarchive.WithAllPlatforms)
			}

			if cliContext.Bool("skip-manifest-json") {
				exportOpts = append(exportOpts, tarchive.WithSkipCompatibilityManifest)
			}

			if cliContext.Bool("skip-non-distributable") {
				exportOpts = append(exportOpts, tarchive.WithSkipNonDistributableBlobs)
			}

			storeOpts := make([]image.StoreOpt, len(images))
			for i, img := range images {
				storeOpts[i] = image.WithExtraReference(img)
			}

			return client.Transfer(ctx,
				image.NewStore("", storeOpts...),
				tarchive.NewImageExportStream(w, "", exportOpts...),
				transfer.WithProgress(pf),
			)
		}

		if pss := cliContext.StringSlice("platform"); len(pss) > 0 {
			all, err := platforms.ParseAll(pss)
			if err != nil {
				return err
			}
			exportOpts = append(exportOpts, archive.WithPlatform(platforms.Ordered(all...)))
		} else {
			exportOpts = append(exportOpts, archive.WithPlatform(platforms.DefaultStrict()))
		}

		if cliContext.Bool("all-platforms") {
			exportOpts = append(exportOpts, archive.WithAllPlatforms())
		}

		if cliContext.Bool("skip-manifest-json") {
			exportOpts = append(exportOpts, archive.WithSkipDockerManifest())
		}

		if cliContext.Bool("skip-non-distributable") {
			exportOpts = append(exportOpts, archive.WithSkipNonDistributableBlobs())
		}

		is := client.ImageService()
		for _, img := range images {
			exportOpts = append(exportOpts, archive.WithImage(is, img))
		}

		return client.Export(ctx, w, exportOpts...)
	},
}
