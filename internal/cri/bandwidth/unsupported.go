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

/*
Copyright 2015 The Kubernetes Authors.

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

package bandwidth

import (
	resource "github.com/basuotian/containerd/internal/cri/resourcequantity"
	"github.com/containerd/errdefs"
)

type unsupportedShaper struct {
}

// NewTCShaper makes a new unsupportedShapper for the given interface
func NewTCShaper(iface string) Shaper {
	return &unsupportedShaper{}
}

func (f *unsupportedShaper) Limit(cidr string, egress, ingress *resource.Quantity) error {
	return errdefs.ErrNotImplemented
}

func (f *unsupportedShaper) Reset(cidr string) error {
	return nil
}

func (f *unsupportedShaper) ReconcileInterface() error {
	return errdefs.ErrNotImplemented
}

func (f *unsupportedShaper) ReconcileCIDR(cidr string, egress, ingress *resource.Quantity) error {
	return errdefs.ErrNotImplemented
}

func (f *unsupportedShaper) GetCIDRs() ([]string, error) {
	return []string{}, nil
}
