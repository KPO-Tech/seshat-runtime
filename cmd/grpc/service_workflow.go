package main

import (
	"context"
	"encoding/json"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// workflowServer runs workflows: a set of prompts linked by the nodes they need.
type workflowServer struct {
	pb.UnimplementedWorkflowServiceServer
	registry *sessionRegistry
}

// NewWorkflowServer builds the workflow service over the server's registry.
func NewWorkflowServer(registry *sessionRegistry) *workflowServer {
	return &workflowServer{registry: registry}
}

var _ pb.WorkflowServiceServer = (*workflowServer)(nil)

// parseWorkflow reads a definition: JSON when asked for, YAML otherwise (YAML
// also accepts JSON, so the format is a hint rather than a constraint).
func parseWorkflow(raw, format string) (sdk.WorkflowDefinition, error) {
	var definition sdk.WorkflowDefinition

	text := strings.TrimSpace(raw)
	if text == "" {
		return definition, status.Error(codes.InvalidArgument, "definition is required")
	}

	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		if err := json.Unmarshal([]byte(text), &definition); err != nil {
			return definition, status.Errorf(codes.InvalidArgument, "parse definition as json: %v", err)
		}
	case "", "yaml", "yml":
		if err := yaml.Unmarshal([]byte(text), &definition); err != nil {
			return definition, status.Errorf(codes.InvalidArgument, "parse definition as yaml: %v", err)
		}
	default:
		return definition, status.Errorf(codes.InvalidArgument, "unknown format %q: use yaml or json", format)
	}
	return definition, nil
}

func (s *workflowServer) ValidateWorkflow(_ context.Context, req *pb.WorkflowDefinitionRequest) (*pb.WorkflowValidationResponse, error) {
	definition, err := parseWorkflow(req.GetDefinition(), req.GetFormat())
	if err != nil {
		// A definition that cannot be read is invalid, not a failed call: the
		// client asked a question and gets the answer with the reason.
		if status.Code(err) == codes.InvalidArgument {
			return &pb.WorkflowValidationResponse{Valid: false, Diagnostics: status.Convert(err).Message()}, nil
		}
		return nil, err
	}
	if err := sdk.ValidateWorkflow(definition); err != nil {
		return &pb.WorkflowValidationResponse{
			Valid:       false,
			Diagnostics: err.Error(),
			Name:        definition.Name,
			NodeCount:   int32(len(definition.Nodes)),
		}, nil
	}
	return &pb.WorkflowValidationResponse{
		Valid:     true,
		Name:      definition.Name,
		NodeCount: int32(len(definition.Nodes)),
	}, nil
}

func (s *workflowServer) DraftWorkflow(_ context.Context, req *pb.DraftWorkflowRequest) (*pb.DraftWorkflowResponse, error) {
	definition, err := parseWorkflow(req.GetDefinition(), req.GetFormat())
	if err != nil {
		return nil, err
	}
	result, err := sdk.DraftWorkflow(sdk.WorkflowDraftOptions{
		Definition: definition,
		Format:     req.GetFormat(),
	})
	response := &pb.DraftWorkflowResponse{
		Rendered:    result.Content,
		Diagnostics: strings.Join(result.Diagnostics, "\n"),
	}
	if err != nil && response.Diagnostics == "" {
		response.Diagnostics = err.Error()
	}
	return response, nil
}

func (s *workflowServer) RunWorkflow(ctx context.Context, req *pb.RunWorkflowRequest) (*pb.WorkflowRunResponse, error) {
	if s.registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "workflows are not available on this server")
	}
	definition, err := parseWorkflow(req.GetDefinition(), "")
	if err != nil {
		return nil, err
	}
	if err := sdk.ValidateWorkflow(definition); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid workflow: %v", err)
	}

	client, release, err := s.workflowClient(req.GetModel())
	if err != nil {
		return nil, err
	}
	defer release()

	result, err := client.RunWorkflow(ctx, definition, sdk.WorkflowOptions{
		MaxParallel: int(req.GetMaxParallel()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "run workflow: %v", err)
	}

	response := &pb.WorkflowRunResponse{
		Success: result.Success,
		Results: make(map[string]*pb.WorkflowNodeResult, len(result.Results)),
		Order:   result.Order,
	}
	for id, node := range result.Results {
		response.Results[id] = &pb.WorkflowNodeResult{
			Id:         node.ID,
			Agent:      node.Agent,
			Success:    node.Success,
			Output:     node.Output,
			Error:      node.Error,
			StartedAt:  timestampFrom(node.StartedAt),
			FinishedAt: timestampFrom(node.EndedAt),
		}
	}
	return response, nil
}

// workflowClient returns the client the nodes run on: the server's own, or one
// built for the model the request names. A model is a property of a client, so
// a run that names one gets its own, released when the run ends.
func (s *workflowServer) workflowClient(model string) (*sdk.Client, func(), error) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		client, err := s.registry.catalogClient()
		if err != nil {
			return nil, nil, status.Errorf(codes.FailedPrecondition, "workflow: %v", err)
		}
		return client, func() {}, nil
	}

	cfg, err := buildSDKClientConfig(s.registry.host, &pb.QueryRequest{Model: trimmed})
	if err != nil {
		return nil, nil, status.Errorf(codes.FailedPrecondition, "workflow: %v", err)
	}
	client, err := sdk.NewClient(cfg)
	if err != nil {
		return nil, nil, status.Errorf(codes.FailedPrecondition, "workflow: %v", err)
	}
	return client, func() { _ = client.Close() }, nil
}
