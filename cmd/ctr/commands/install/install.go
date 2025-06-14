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

package install

import (
	containerd "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/cmd/ctr/commands"
	"github.com/urfave/cli/v2"
)

// Command to install binary packages
var Command = &cli.Command{
	Name:        "install",
	Usage:       "Install a new package",
	ArgsUsage:   "<ref>",
	Description: "install a new package",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "libs",
			Aliases: []string{"l"},
			Usage:   "Install libs from the image",
		},
		&cli.BoolFlag{
			Name:    "replace",
			Aliases: []string{"r"},
			Usage:   "Replace any binaries or libs in the opt directory",
		},
		&cli.StringFlag{
			Name:  "path",
			Usage: "Set an optional install path other than the managed opt directory",
		},
	},
	Action: func(cliContext *cli.Context) error {
		client, ctx, cancel, err := commands.NewClient(cliContext)
		if err != nil {
			return err
		}
		defer cancel()
		ref := cliContext.Args().First()
		image, err := client.GetImage(ctx, ref)
		if err != nil {
			return err
		}
		var opts []containerd.InstallOpts
		if cliContext.Bool("libs") {
			opts = append(opts, containerd.WithInstallLibs)
		}
		if cliContext.Bool("replace") {
			opts = append(opts, containerd.WithInstallReplace)
		}
		if path := cliContext.String("path"); path != "" {
			opts = append(opts, containerd.WithInstallPath(path))
		}
		return client.Install(ctx, image, opts...)
	},
}
