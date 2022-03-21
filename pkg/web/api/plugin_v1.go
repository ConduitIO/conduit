package api

import (
	"context"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin"
	"github.com/conduitio/conduit/pkg/web/api/status"
	"github.com/conduitio/conduit/pkg/web/api/toproto"
	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
	"regexp"
)

//go:generate mockgen -destination=mock/plugin.go -package=mock -mock_names=PluginOrchestrator=PluginOrchestrator . PluginOrchestrator

// PluginOrchestrator defines a CRUD interface that manages the Plugin resource.
type PluginOrchestrator interface {
	// List will return all plugins' specs.
	List(ctx context.Context) (map[string]plugin.Specification, error)
}

type PluginAPIv1 struct {
	apiv1.UnimplementedPluginServiceServer
	ps PluginOrchestrator
}

func NewPluginAPIv1(ps PluginOrchestrator) *PluginAPIv1 {
	return &PluginAPIv1{ps: ps}
}

func (p *PluginAPIv1) Register(srv *grpc.Server) {
	apiv1.RegisterPluginServiceServer(srv, p)
}

func (p *PluginAPIv1) ListPlugins(
	ctx context.Context,
	req *apiv1.ListPluginsRequest,
) (*apiv1.ListPluginsResponse, error) {

	var nameFilter *regexp.Regexp
	if req.GetName() != "" {
		var err error
		nameFilter, err = regexp.Compile("^" + req.GetName() + "$")
		if err != nil {
			// todo: make plugin error
			return nil, status.PipelineError(cerrors.New("invalid name regex"))
		}
	}

	list, _ := p.ps.List(ctx)
	var plist []*apiv1.PluginSpecifications

	for _, v := range list {
		if nameFilter != nil && !nameFilter.MatchString(v.Name) {
			continue // don't add to result list, filter didn't match
		}
		plist = append(plist, toproto.Plugin(&v))
	}

	return &apiv1.ListPluginsResponse{Plugins: plist}, nil
}
