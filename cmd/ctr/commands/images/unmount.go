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
	"github.com/basuotian/containerd/core/leases"
	"github.com/basuotian/containerd/core/mount"
	"github.com/containerd/errdefs"
	"github.com/urfave/cli/v2"
)

var unmountCommand = &cli.Command{
	Name:        "unmount",
	Usage:       "Unmount the image from the target",
	ArgsUsage:   "[flags] <target>",
	Description: "Unmount the image rootfs from the specified target.",
	Flags: append(append(commands.RegistryFlags, append(commands.SnapshotterFlags, commands.LabelFlag)...),
		&cli.BoolFlag{
			Name:  "rm",
			Usage: "Remove the snapshot after a successful unmount",
		},
	),
	Action: func(cliContext *cli.Context) error {
		var (
			target = cliContext.Args().First()
		)
		if target == "" {
			return errors.New("please provide a target path to unmount from")
		}

		client, ctx, cancel, err := commands.NewClient(cliContext)
		if err != nil {
			return err
		}
		defer cancel()

		if err := mount.UnmountAll(target, 0); err != nil {
			return err
		}

		if cliContext.Bool("rm") {
			snapshotter := cliContext.String("snapshotter")
			s := client.SnapshotService(snapshotter)
			if err := client.LeasesService().Delete(ctx, leases.Lease{ID: target}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("error deleting lease: %w", err)
			}
			if err := s.Remove(ctx, target); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("error removing snapshot: %w", err)
			}
		}

		fmt.Fprintln(cliContext.App.Writer, target)
		return nil
	},
}
