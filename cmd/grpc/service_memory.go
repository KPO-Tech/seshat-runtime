package main

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	longterm "github.com/KPO-Tech/seshat-runtime/internal/memory/longterm"
	pb "github.com/KPO-Tech/seshat-runtime/pkg/grpc/seshat"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
)

// memoryServer is the long-term memory of the runtime: entities, their
// observations, the relations between them, and the block injected for a query.
type memoryServer struct {
	pb.UnimplementedMemoryServiceServer
	registry *sessionRegistry
}

// NewMemoryServer builds the memory service over the server's registry, whose
// catalog client owns the memory store.
func NewMemoryServer(registry *sessionRegistry) *memoryServer {
	return &memoryServer{registry: registry}
}

var _ pb.MemoryServiceServer = (*memoryServer)(nil)

// store returns the memory store, or the reason there is none.
func (s *memoryServer) store() (sdk.LongTermMemory, error) {
	if s.registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "memory is not available on this server")
	}
	client, err := s.registry.catalogClient()
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "memory: %v", err)
	}
	store := client.LongTermMemory()
	if store == nil {
		return nil, status.Error(codes.FailedPrecondition,
			"long-term memory is not enabled on this server: set SESHAT_MEMORY_ENABLED=true and restart it")
	}
	return store, nil
}

func (s *memoryServer) UpsertEntities(ctx context.Context, req *pb.UpsertEntitiesRequest) (*pb.UpsertEntitiesResponse, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	inputs := make([]longterm.EntityInput, 0, len(req.GetEntities()))
	for _, entity := range req.GetEntities() {
		if entity == nil {
			continue
		}
		inputs = append(inputs, longterm.EntityInput{
			Name:         entity.GetName(),
			EntityType:   entity.GetEntityType(),
			Observations: entity.GetObservations(),
		})
	}
	if len(inputs) == 0 {
		return nil, status.Error(codes.InvalidArgument, "entities is required")
	}
	created, err := store.UpsertEntities(ctx, req.GetUserId(), inputs)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert entities: %v", err)
	}
	response := &pb.UpsertEntitiesResponse{}
	for _, entity := range created {
		response.Created = append(response.Created, memoryEntityToProto(entity))
	}
	return response, nil
}

func (s *memoryServer) AddObservations(ctx context.Context, req *pb.AddObservationsRequest) (*pb.AddObservationsResponse, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	inputs := make([]longterm.ObservationInput, 0, len(req.GetObservations()))
	for _, observation := range req.GetObservations() {
		if observation == nil {
			continue
		}
		inputs = append(inputs, longterm.ObservationInput{
			EntityName: observation.GetEntityName(),
			Contents:   observation.GetContents(),
		})
	}
	if len(inputs) == 0 {
		return nil, status.Error(codes.InvalidArgument, "observations is required")
	}
	results, err := store.AddObservations(ctx, req.GetUserId(), inputs)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "add observations: %v", err)
	}
	response := &pb.AddObservationsResponse{}
	for _, result := range results {
		response.Results = append(response.Results, &pb.ObservationResult{
			EntityName:        result.EntityName,
			AddedObservations: result.AddedObservations,
		})
	}
	return response, nil
}

func (s *memoryServer) SearchMemory(ctx context.Context, req *pb.SearchMemoryRequest) (*pb.MemoryGraph, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	graph, err := store.SearchNodes(ctx, req.GetUserId(), req.GetQuery())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "search memory: %v", err)
	}
	return memoryGraphToProto(graph), nil
}

func (s *memoryServer) OpenEntities(ctx context.Context, req *pb.OpenEntitiesRequest) (*pb.MemoryGraph, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	if len(req.GetNames()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "names is required")
	}
	graph, err := store.OpenNodes(ctx, req.GetUserId(), req.GetNames())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "open entities: %v", err)
	}
	return memoryGraphToProto(graph), nil
}

func (s *memoryServer) RetrieveContext(ctx context.Context, req *pb.RetrieveContextRequest) (*pb.RetrieveContextResponse, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	contextBlock, err := store.RetrieveForContext(ctx, req.GetUserId(), req.GetQuery(), int(req.GetMaxTokens()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "retrieve memory: %v", err)
	}
	return &pb.RetrieveContextResponse{Context: contextBlock, Found: contextBlock != ""}, nil
}

func memoryEntityToProto(entity longterm.Entity) *pb.MemoryEntity {
	return &pb.MemoryEntity{
		Id:           entity.ID,
		UserId:       entity.UserID,
		Name:         entity.Name,
		EntityType:   entity.EntityType,
		Observations: entity.Observations,
		CreatedAt:    timestampFrom(entity.CreatedAt),
		UpdatedAt:    timestampFrom(entity.UpdatedAt),
	}
}

func memoryGraphToProto(graph *longterm.Graph) *pb.MemoryGraph {
	out := &pb.MemoryGraph{}
	if graph == nil {
		return out
	}
	for _, entity := range graph.Entities {
		out.Entities = append(out.Entities, memoryEntityToProto(entity))
	}
	for _, relation := range graph.Relations {
		out.Relations = append(out.Relations, &pb.MemoryRelation{
			Id:           relation.ID,
			UserId:       relation.UserID,
			From:         relation.From,
			To:           relation.To,
			RelationType: relation.RelationType,
			CreatedAt:    timestampFrom(relation.CreatedAt),
		})
	}
	return out
}
