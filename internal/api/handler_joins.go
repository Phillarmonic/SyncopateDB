package api

import (
	"encoding/json"
	"github.com/phillarmonic/syncopate-db/internal/common"
	"github.com/phillarmonic/syncopate-db/internal/datastore"
	"github.com/phillarmonic/syncopate-db/internal/errors"
	"net/http"
	"strings"
)

// handleNestedQuery executes a nested join query
func (s *Server) handleNestedQuery(w http.ResponseWriter, r *http.Request) {
	var queryOpts datastore.QueryOptions
	if err := json.NewDecoder(r.Body).Decode(&queryOpts); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid request payload",
			errors.NewError(errors.ErrCodeMalformedData, "Failed to decode query options"))
		return
	}
	defer r.Body.Close()

	// Validate that we have at least one join
	if len(queryOpts.Joins) == 0 {
		s.respondWithError(w, http.StatusBadRequest, "No joins specified for nested query",
			errors.NewError(errors.ErrCodeInvalidJoin, "No joins specified for nested query"))
		return
	}

	// Use the new function that properly handles joins without modifying original entities
	response, err := s.queryService.ExecuteQueryWithJoins(queryOpts)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			datastore.ConvertToSyncopateError(err))
		return
	}

	// Get the entity definition to determine ID type for the main entities
	def, err := s.engine.GetEntityDefinition(queryOpts.EntityType)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			datastore.ConvertToSyncopateError(err))
		return
	}

	// Convert and filter the response
	filteredData := make([]interface{}, len(response.Data))
	for i, entity := range response.Data {
		// Create a filtered copy of the entity
		filteredEntity := common.Entity{
			ID:     entity.ID,
			Type:   entity.Type,
			Fields: make(map[string]interface{}),
		}

		// Copy non-internal fields and preserve join fields
		for name, value := range entity.Fields {
			// Keep all fields that don't start with underscore OR
			// fields that match join aliases in the query
			if !strings.HasPrefix(name, "_") || s.isJoinField(name, queryOpts.Joins) {
				filteredEntity.Fields[name] = value
			}
		}

		// Ensure all fields from definition are included
		completeEntity := s.includeAllDefinedFields(filteredEntity, def)

		// Then convert to proper representation with correct ID type
		filteredData[i] = common.ConvertToRepresentation(completeEntity, def.IDGenerator)
	}

	// Create the final response
	convertedResponse := struct {
		Total      int           `json:"total"`
		Count      int           `json:"count"`
		Limit      int           `json:"limit"`
		Offset     int           `json:"offset"`
		HasMore    bool          `json:"hasMore"`
		EntityType string        `json:"entityType"`
		Data       []interface{} `json:"data"`
	}{
		Total:      response.Total,
		Count:      response.Count,
		Limit:      response.Limit,
		Offset:     response.Offset,
		HasMore:    response.HasMore,
		EntityType: response.EntityType,
		Data:       filteredData,
	}

	s.respondWithJSON(w, http.StatusOK, convertedResponse)
}

// Helper function to check if a field is a join alias
func (s *Server) isJoinField(fieldName string, joins []datastore.JoinOptions) bool {
	for _, join := range joins {
		if fieldName == join.As {
			return true
		}
	}
	return false
}
