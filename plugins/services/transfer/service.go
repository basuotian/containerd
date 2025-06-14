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

package transfer

import (
	"context"

	transferapi "github.com/containerd/containerd/api/services/transfer/v1"
	"github.com/containerd/containerd/api/types"
	transferTypes "github.com/containerd/containerd/api/types/transfer"
	"github.com/containerd/errdefs"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	"github.com/containerd/typeurl/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/basuotian/containerd/core/streaming"
	"github.com/basuotian/containerd/core/transfer"
	tplugins "github.com/basuotian/containerd/core/transfer/plugins"
	"github.com/basuotian/containerd/pkg/oci"
	ptypes "github.com/basuotian/containerd/pkg/protobuf/types"
	"github.com/basuotian/containerd/plugins"
)

var empty = &ptypes.Empty{}

func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.GRPCPlugin,
		ID:   "transfer",
		Requires: []plugin.Type{
			plugins.TransferPlugin,
			plugins.StreamingPlugin,
		},
		InitFn: newService,
	})
}

type service struct {
	transferrers  []transfer.Transferrer
	streamManager streaming.StreamManager
	transferapi.UnimplementedTransferServer
}

func newService(ic *plugin.InitContext) (interface{}, error) {
	sps, err := ic.GetByType(plugins.TransferPlugin)
	if err != nil {
		return nil, err
	}

	// TODO: how to determine order?
	t := make([]transfer.Transferrer, 0, len(sps))
	for _, p := range sps {
		t = append(t, p.(transfer.Transferrer))
	}
	sp, err := ic.GetByID(plugins.StreamingPlugin, "manager")
	if err != nil {
		return nil, err
	}
	return &service{
		transferrers:  t,
		streamManager: sp.(streaming.StreamManager),
	}, nil
}

func (s *service) Register(gs *grpc.Server) error {
	transferapi.RegisterTransferServer(gs, s)
	return nil
}

func (s *service) Transfer(ctx context.Context, req *transferapi.TransferRequest) (*emptypb.Empty, error) {
	var transferOpts []transfer.Opt
	if req.Options != nil {
		if req.Options.ProgressStream != "" {
			stream, err := s.streamManager.Get(ctx, req.Options.ProgressStream)
			if err != nil {
				return nil, errgrpc.ToGRPC(err)
			}
			defer stream.Close()

			pf := func(p transfer.Progress) {
				var descp *types.Descriptor
				if p.Desc != nil {
					descp = oci.DescriptorToProto(*p.Desc)
				}
				progress, err := typeurl.MarshalAny(&transferTypes.Progress{
					Event:    p.Event,
					Name:     p.Name,
					Parents:  p.Parents,
					Progress: p.Progress,
					Total:    p.Total,
					Desc:     descp,
				})
				if err != nil {
					log.G(ctx).WithError(err).Warnf("event could not be marshaled: %v/%v", p.Event, p.Name)
					return
				}
				if err := stream.Send(progress); err != nil {
					log.G(ctx).WithError(err).Warnf("event not sent: %v/%v", p.Event, p.Name)
					return
				}
			}

			transferOpts = append(transferOpts, transfer.WithProgress(pf))
		}
	}
	src, err := s.convertAny(ctx, req.Source)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}
	dst, err := s.convertAny(ctx, req.Destination)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	for _, t := range s.transferrers {
		if err := t.Transfer(ctx, src, dst, transferOpts...); err == nil {
			return empty, nil
		} else if !errdefs.IsNotImplemented(err) {
			return nil, errgrpc.ToGRPC(err)
		}
		log.G(ctx).WithError(err).Debugf("transfer not implemented for %T to %T", src, dst)
	}
	return nil, status.Errorf(codes.Unimplemented, "method Transfer not implemented for %s to %s", req.Source.GetTypeUrl(), req.Destination.GetTypeUrl())
}

func (s *service) convertAny(ctx context.Context, a typeurl.Any) (interface{}, error) {
	obj, err := tplugins.ResolveType(a)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return typeurl.UnmarshalAny(a)
		}
		return nil, err
	}
	switch v := obj.(type) {
	case streamUnmarshaler:
		err = v.UnmarshalAny(ctx, s.streamManager, a)
		return obj, err
	default:
		log.G(ctx).Debug("unmarshling to..")
		err = typeurl.UnmarshalTo(a, obj)
		return obj, err
	}
}

type streamUnmarshaler interface {
	UnmarshalAny(context.Context, streaming.StreamGetter, typeurl.Any) error
}
