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

package app

import (
	"fmt"
	"io"

	"github.com/containerd/log"
	"github.com/urfave/cli/v2"
	"google.golang.org/grpc/grpclog"

	"github.com/basuotian/containerd/cmd/ctr/commands/containers"
	"github.com/basuotian/containerd/cmd/ctr/commands/content"
	"github.com/basuotian/containerd/cmd/ctr/commands/deprecations"
	"github.com/basuotian/containerd/cmd/ctr/commands/events"
	"github.com/basuotian/containerd/cmd/ctr/commands/images"
	"github.com/basuotian/containerd/cmd/ctr/commands/info"
	"github.com/basuotian/containerd/cmd/ctr/commands/install"
	"github.com/basuotian/containerd/cmd/ctr/commands/leases"
	namespacesCmd "github.com/basuotian/containerd/cmd/ctr/commands/namespaces"
	ociCmd "github.com/basuotian/containerd/cmd/ctr/commands/oci"
	"github.com/basuotian/containerd/cmd/ctr/commands/plugins"
	"github.com/basuotian/containerd/cmd/ctr/commands/pprof"
	"github.com/basuotian/containerd/cmd/ctr/commands/run"
	"github.com/basuotian/containerd/cmd/ctr/commands/sandboxes"
	"github.com/basuotian/containerd/cmd/ctr/commands/snapshots"
	"github.com/basuotian/containerd/cmd/ctr/commands/tasks"
	versionCmd "github.com/basuotian/containerd/cmd/ctr/commands/version"
	"github.com/basuotian/containerd/defaults"
	"github.com/basuotian/containerd/pkg/namespaces"
	"github.com/basuotian/containerd/version"
)

var extraCmds = []*cli.Command{}

func init() {
	// Discard grpc logs so that they don't mess with our stdio
	grpclog.SetLoggerV2(grpclog.NewLoggerV2(io.Discard, io.Discard, io.Discard))

	cli.VersionPrinter = func(cliContext *cli.Context) {
		fmt.Println(cliContext.App.Name, version.Package, cliContext.App.Version)
	}
	cli.VersionFlag = &cli.BoolFlag{
		Name:    "version",
		Aliases: []string{"v"},
		Usage:   "Print the version",
	}
	cli.HelpFlag = &cli.BoolFlag{
		Name:    "help",
		Aliases: []string{"h"},
		Usage:   "Show help",
	}
}

// New returns a *cli.App instance.
func New() *cli.App {
	app := cli.NewApp()
	app.Name = "ctr"
	app.Version = version.Version
	app.Description = `
ctr is an unsupported debug and administrative client for interacting
with the containerd daemon. Because it is unsupported, the commands,
options, and operations are not guaranteed to be backward compatible or
stable from release to release of the containerd project.`
	app.Usage = `
        __
  _____/ /______
 / ___/ __/ ___/
/ /__/ /_/ /
\___/\__/_/

containerd CLI
`
	app.DisableSliceFlagSeparator = true
	app.EnableBashCompletion = true
	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:  "debug",
			Usage: "Enable debug output in logs",
		},
		&cli.StringFlag{
			Name:    "address",
			Aliases: []string{"a"},
			Usage:   "Address for containerd's GRPC server",
			Value:   defaults.DefaultAddress,
			EnvVars: []string{"CONTAINERD_ADDRESS"},
		},
		&cli.DurationFlag{
			Name:  "timeout",
			Usage: "Total timeout for ctr commands",
		},
		&cli.DurationFlag{
			Name:  "connect-timeout",
			Usage: "Timeout for connecting to containerd",
		},
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Namespace to use with commands",
			Value:   namespaces.Default,
			EnvVars: []string{namespaces.NamespaceEnvVar},
		},
	}
	app.Commands = append([]*cli.Command{
		plugins.Command,
		versionCmd.Command,
		containers.Command,
		content.Command,
		events.Command,
		images.Command,
		leases.Command,
		namespacesCmd.Command,
		pprof.Command,
		run.Command,
		snapshots.Command,
		tasks.Command,
		install.Command,
		ociCmd.Command,
		sandboxes.Command,
		info.Command,
		deprecations.Command,
	}, extraCmds...)
	app.Before = func(cliContext *cli.Context) error {
		if cliContext.Bool("debug") {
			return log.SetLevel("debug")
		}
		return nil
	}
	return app
}
