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

package tasks

import (
	"github.com/basuotian/containerd/cmd/ctr/commands"
	"github.com/urfave/cli/v2"
)

var resumeCommand = &cli.Command{
	Name:      "resume",
	Usage:     "Resume a paused container",
	ArgsUsage: "CONTAINER",
	Action: func(cliContext *cli.Context) error {
		client, ctx, cancel, err := commands.NewClient(cliContext)
		if err != nil {
			return err
		}
		defer cancel()
		container, err := client.LoadContainer(ctx, cliContext.Args().First())
		if err != nil {
			return err
		}
		task, err := container.Task(ctx, nil)
		if err != nil {
			return err
		}
		return task.Resume(ctx)
	},
}
