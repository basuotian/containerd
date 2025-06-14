//go:build !linux

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

package apparmor

import (
	"context"
	"errors"

	"github.com/basuotian/containerd/core/containers"
	"github.com/basuotian/containerd/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// WithProfile sets the provided apparmor profile to the spec
func WithProfile(profile string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		return errors.New("apparmor is not supported")
	}
}

// WithDefaultProfile will generate a default apparmor profile under the provided name
// for the container.  It is only generated if a profile under that name does not exist.
func WithDefaultProfile(name string) oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		return errors.New("apparmor is not supported")
	}
}
