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
	"errors"

	containerd "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/cmd/ctr/commands"
	"github.com/basuotian/containerd/pkg/cio"
	"github.com/containerd/console"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"
	"github.com/urfave/cli/v2"
)

var startCommand = &cli.Command{
	Name:      "start",
	Usage:     "Start a container that has been created",
	ArgsUsage: "CONTAINER",
	Flags: append(platformStartFlags, []cli.Flag{
		&cli.BoolFlag{
			Name:  "null-io",
			Usage: "Send all IO to /dev/null",
		},
		&cli.StringFlag{
			Name:  "log-uri",
			Usage: "Log uri",
		},
		&cli.StringFlag{
			Name:  "fifo-dir",
			Usage: "Directory used for storing IO FIFOs",
		},
		&cli.StringFlag{
			Name:  "pid-file",
			Usage: "File path to write the task's pid",
		},
		&cli.BoolFlag{
			Name:    "detach",
			Aliases: []string{"d"},
			Usage:   "Detach from the task after it has started execution",
		},
	}...),
	Action: func(cliContext *cli.Context) error {
		var (
			err    error
			id     = cliContext.Args().Get(0)
			detach = cliContext.Bool("detach")
		)
		if id == "" {
			return errors.New("container id must be provided")
		}
		client, ctx, cancel, err := commands.NewClient(cliContext)
		if err != nil {
			return err
		}
		defer cancel()
		container, err := client.LoadContainer(ctx, id)
		if err != nil {
			return err
		}

		spec, err := container.Spec(ctx)
		if err != nil {
			return err
		}
		var (
			tty    = spec.Process.Terminal
			opts   = GetNewTaskOpts(cliContext)
			ioOpts = []cio.Opt{cio.WithFIFODir(cliContext.String("fifo-dir"))}
		)
		var con console.Console
		if tty {
			con = console.Current()
			defer con.Reset()
			if err := con.SetRaw(); err != nil {
				return err
			}
		}

		task, err := NewTask(ctx, client, container, "", con, cliContext.Bool("null-io"), cliContext.String("log-uri"), ioOpts, opts...)
		if err != nil {
			return err
		}
		var statusC <-chan containerd.ExitStatus
		if !detach {
			defer func() {
				if _, err := task.Delete(ctx, containerd.WithProcessKill); err != nil && !errdefs.IsNotFound(err) {
					log.L.WithError(err).Error("failed to cleanup task")
				}
			}()

			if statusC, err = task.Wait(ctx); err != nil {
				return err
			}
		}
		if cliContext.IsSet("pid-file") {
			if err := commands.WritePidFile(cliContext.String("pid-file"), int(task.Pid())); err != nil {
				return err
			}
		}

		if err := task.Start(ctx); err != nil {
			return err
		}
		if detach {
			return nil
		}
		if tty {
			if err := HandleConsoleResize(ctx, task, con); err != nil {
				log.L.WithError(err).Error("console resize")
			}
		} else {
			sigc := commands.ForwardAllSignals(ctx, task)
			defer commands.StopCatch(sigc)
		}

		status := <-statusC
		code, _, err := status.Result()
		if err != nil {
			return err
		}
		if _, err := task.Delete(ctx); err != nil {
			return err
		}
		if code != 0 {
			return cli.Exit("", int(code))
		}
		return nil
	},
}
