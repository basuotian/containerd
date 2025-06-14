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
	"context"
	"errors"
	"net/url"
	"time"

	containerd "github.com/basuotian/containerd/client"
	"github.com/basuotian/containerd/pkg/cio"
	"github.com/containerd/console"
	"github.com/containerd/log"
	"github.com/urfave/cli/v2"
)

var platformStartFlags = []cli.Flag{}

// HandleConsoleResize resizes the console
func HandleConsoleResize(ctx context.Context, task resizer, con console.Console) error {
	// do an initial resize of the console
	size, err := con.Size()
	if err != nil {
		return err
	}
	go func() {
		prevSize := size
		for {
			time.Sleep(time.Millisecond * 250)

			size, err := con.Size()
			if err != nil {
				log.G(ctx).WithError(err).Error("get pty size")
				continue
			}

			if size.Width != prevSize.Width || size.Height != prevSize.Height {
				if err := task.Resize(ctx, uint32(size.Width), uint32(size.Height)); err != nil {
					log.G(ctx).WithError(err).Error("resize pty")
				}
				prevSize = size
			}
		}
	}()
	return nil
}

// NewTask creates a new task
func NewTask(ctx context.Context, client *containerd.Client, container containerd.Container, _ string, con console.Console, nullIO bool, logURI string, ioOpts []cio.Opt, opts ...containerd.NewTaskOpts) (containerd.Task, error) {
	var ioCreator cio.Creator
	if con != nil {
		if nullIO {
			return nil, errors.New("tty and null-io cannot be used together")
		}
		ioCreator = cio.NewCreator(append([]cio.Opt{cio.WithStreams(con, con, nil), cio.WithTerminal}, ioOpts...)...)
	} else if nullIO {
		ioCreator = cio.NullIO
	} else if logURI != "" {
		u, err := url.Parse(logURI)
		if err != nil {
			return nil, err
		}
		ioCreator = cio.LogURI(u)
	} else {
		ioCreator = cio.NewCreator(append([]cio.Opt{cio.WithStdio}, ioOpts...)...)
	}
	return container.NewTask(ctx, ioCreator)
}

// GetNewTaskOpts resolves containerd.NewTaskOpts from cli.Context
func GetNewTaskOpts(_ *cli.Context) []containerd.NewTaskOpts {
	return nil
}
