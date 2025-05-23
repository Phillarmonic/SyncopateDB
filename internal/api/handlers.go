package api

import (
	"encoding/json"
	"fmt"
	"github.com/phillarmonic/syncopate-db/internal/about"
	"github.com/phillarmonic/syncopate-db/internal/errors"
	"github.com/phillarmonic/syncopate-db/internal/settings"
	"github.com/sirupsen/logrus"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/phillarmonic/syncopate-db/internal/common"
	"github.com/phillarmonic/syncopate-db/internal/datastore"
)

type WelcomeResponse struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Description   string `json:"description"`
	Documentation string `json:"documentation"`
	HealthCheck   string `json:"healthCheck"`
	Status        string `json:"status"`
	ServerTime    string `json:"serverTime"`
}

// handleSettings returns the current configuration settings
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	// Create a settings view that's safe to expose
	settingsView := map[string]interface{}{
		"debug":         settings.Config.Debug,
		"logLevel":      settings.Config.LogLevel,
		"port":          settings.Config.Port,
		"enableWAL":     settings.Config.EnableWAL,
		"enableZSTD":    settings.Config.EnableZSTD,
		"colorizedLogs": settings.Config.ColorizedLogs,
		"serverTime":    time.Now().Format(time.RFC3339),
		"version":       about.About().Version,
		"environment":   determineEnvironment(),
	}

	s.respondWithJSON(w, http.StatusOK, settingsView, true)
}

// handleWelcome provides a welcome message for the root path
func (s *Server) handleWelcome(w http.ResponseWriter, r *http.Request) {
	welcomeMessage := WelcomeResponse{
		Name:          about.About().Name,
		Version:       about.About().Version,
		Description:   about.About().Description,
		Documentation: "/api/v1",
		HealthCheck:   "/health",
		Status:        "running",
		ServerTime:    time.Now().Format(time.RFC3339),
	}

	// Use pretty-printed JSON for the welcome message
	s.respondWithJSON(w, http.StatusOK, welcomeMessage, true)
}

// handleHealthCheck handles health check requests
func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	s.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetEntityTypes lists all entity types
func (s *Server) handleGetEntityTypes(w http.ResponseWriter, r *http.Request) {
	types := s.engine.ListEntityTypes()
	s.respondWithJSON(w, http.StatusOK, types)
}

// handleCreateEntityType creates a new entity type
func (s *Server) handleCreateEntityType(w http.ResponseWriter, r *http.Request) {
	var def common.EntityDefinition
	if err := json.NewDecoder(r.Body).Decode(&def); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid request payload",
			errors.NewError(errors.ErrCodeMalformedData, "Failed to decode entity definition"))
		return
	}
	defer r.Body.Close()

	// Note: If IDGenerator is an empty string, auto_increment will be used as default
	if err := s.engine.RegisterEntityType(def); err != nil {
		// Convert to SyncopateError if it's not already
		synErr := datastore.ConvertToSyncopateError(err)

		// Map error code to HTTP status
		statusCode := http.StatusBadRequest
		if errors.IsErrorCode(synErr, errors.ErrCodeEntityTypeExists) {
			statusCode = http.StatusConflict
		}

		s.respondWithError(w, statusCode, err.Error(), synErr)
		return
	}

	// Get the actual definition with any defaults applied
	updatedDef, err := s.engine.GetEntityDefinition(def.Name)
	if err != nil {
		s.respondWithError(w, http.StatusInternalServerError,
			"Entity type created but could not retrieve it",
			errors.NewError(errors.ErrCodeEntityTypeNotFound, err.Error()))
		return
	}

	s.respondWithJSON(w, http.StatusCreated, map[string]interface{}{
		"message":    "Entity type created successfully",
		"entityType": updatedDef,
	})
}

// handleGetEntityType retrieves a specific entity type
func (s *Server) handleGetEntityType(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	def, err := s.engine.GetEntityDefinition(name)
	if err != nil {
		s.respondWithError(w, http.StatusNotFound, err.Error(),
			errors.NewError(errors.ErrCodeEntityTypeNotFound, fmt.Sprintf("Entity type '%s' not found", name)))
		return
	}

	s.respondWithJSON(w, http.StatusOK, def)
}

// handleListEntities lists entities of a specific type
func (s *Server) handleListEntities(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	entityType := vars["type"]

	// Parse query parameters
	limit, offset, orderBy, orderDesc := s.parseQueryParams(r)

	// Create query options
	queryOpts := datastore.QueryOptions{
		EntityType: entityType,
		Limit:      limit,
		Offset:     offset,
		OrderBy:    orderBy,
		OrderDesc:  orderDesc,
	}

	// Execute query
	response, err := s.queryService.ExecutePaginatedQuery(queryOpts)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			datastore.ConvertToSyncopateError(err))
		return
	}

	// Get the entity definition to determine ID type
	def, err := s.engine.GetEntityDefinition(entityType)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			datastore.ConvertToSyncopateError(err))
		return
	}

	// Filter internal fields from response data, ensure all fields are included, and convert IDs
	filteredData := make([]interface{}, len(response.Data))
	for i, entity := range response.Data {
		// Filter internal fields first
		filteredEntity := s.filterInternalFields(entity)
		// Ensure all fields from definition are included
		completeEntity := s.includeAllDefinedFields(filteredEntity, def)
		// Then convert to representation with proper ID type
		filteredData[i] = common.ConvertToRepresentation(completeEntity, def.IDGenerator)
	}

	// Create a new response with the filtered and converted data
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

// handleCreateEntity creates a new entity
// Todo user should not be able to point an ID on auto increment on create/update
func (s *Server) handleCreateEntity(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	entityType := vars["type"]

	var entityData struct {
		ID     string                 `json:"id"`
		Fields map[string]interface{} `json:"fields"`
	}

	if err := json.NewDecoder(r.Body).Decode(&entityData); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid request payload",
			errors.NewError(errors.ErrCodeMalformedData, "Failed to decode entity data"))
		return
	}
	defer r.Body.Close()

	// Get entity definition to check the ID generator type
	def, err := s.engine.GetEntityDefinition(entityType)
	if err != nil {
		synErr := datastore.ConvertToSyncopateError(err)
		s.respondWithError(w, http.StatusBadRequest, err.Error(), synErr)
		return
	}

	// Custom ID is required only if the ID generation type is custom
	if entityData.ID == "" && def.IDGenerator == common.IDTypeCustom {
		s.respondWithError(w, http.StatusBadRequest, "Entity ID is required for custom ID generation",
			errors.NewError(errors.ErrCodeRequiredFieldMissing, "Entity ID is required for custom ID generation"))
		return
	}

	// Convert ID to string if provided as a number for auto_increment
	// (This is a defensive measure in case the client sends a numeric ID)
	rawID := entityData.ID

	// Insert the entity - ID will be generated if not provided
	if err := s.engine.Insert(entityType, rawID, entityData.Fields); err != nil {
		synErr := datastore.ConvertToSyncopateError(err)

		// Map specific error types to appropriate HTTP status codes
		statusCode := http.StatusBadRequest
		if errors.IsErrorCode(synErr, errors.ErrCodeUniqueConstraint) {
			statusCode = http.StatusConflict
		} else if errors.IsErrorCode(synErr, errors.ErrCodeEntityTypeNotFound) {
			statusCode = http.StatusNotFound
		}

		s.respondWithError(w, statusCode, err.Error(), synErr)
		return
	}

	// For auto-generated IDs, we need to find the ID that was generated
	var responseID interface{}

	if rawID == "" {
		// We need to find the entity that was just inserted
		// This is a bit inefficient, but it works for the response
		// A better approach would be to modify Insert to return the generated ID
		entities, err := s.engine.GetAllEntitiesOfType(entityType)
		if err != nil {
			s.respondWithError(w, http.StatusInternalServerError, "Failed to retrieve entity after creation",
				errors.NewError(errors.ErrCodeInternalServer, "Failed to retrieve entity after creation"))
			return
		}

		// Find the most recently inserted entity by looking at _created_at timestamp
		var newestEntity common.Entity
		var newestTime time.Time

		for _, e := range entities {
			if createdAt, ok := e.Fields["_created_at"].(time.Time); ok {
				if newestEntity.ID == "" || createdAt.After(newestTime) {
					newestEntity = e
					newestTime = createdAt
				}
			}
		}

		if newestEntity.ID != "" {
			rawID = newestEntity.ID
		}
	}

	// Format the response ID based on entity type's ID generator
	responseID = rawID

	// For auto_increment, convert ID to int for the response
	if def.IDGenerator == common.IDTypeAutoIncrement {
		if id, err := strconv.Atoi(rawID); err == nil {
			responseID = id
		}
	}

	s.respondWithJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "Entity created successfully",
		"id":      responseID,
	})
}

// handleGetEntity retrieves a specific entity
func (s *Server) handleGetEntity(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	rawID := vars["id"]
	entityType := vars["type"]

	// Normalize the ID based on entity type's ID generator
	normalizedID, err := s.normalizeEntityID(entityType, rawID)
	if err != nil {
		synErr := datastore.ConvertToSyncopateError(err)
		s.respondWithError(w, http.StatusBadRequest, err.Error(), synErr)
		return
	}

	// Add debugging information in development mode
	if s.config.DebugMode {
		s.logger.WithFields(logrus.Fields{
			"entityType":   entityType,
			"rawID":        rawID,
			"normalizedID": normalizedID,
		}).Debug("Getting entity")
	}

	// Use a type-specific get method if available
	var entity common.Entity
	var getErr error

	if engine, ok := s.engine.(*datastore.Engine); ok {
		entity, getErr = engine.GetByType(normalizedID, entityType)
	} else {
		// Fallback for other implementations
		entity, getErr = s.engine.Get(normalizedID)
		// Check the type matches what we're looking for
		if getErr == nil && entity.Type != entityType {
			getErr = datastore.EntityNotFoundError(entityType, normalizedID)
		}
	}

	if getErr != nil {
		synErr := datastore.ConvertToSyncopateError(getErr)
		statusCode := http.StatusNotFound

		// For certain error types, use a different status code
		if errors.IsErrorCode(synErr, errors.ErrCodeInvalidID) {
			statusCode = http.StatusBadRequest
		}

		s.respondWithError(w, statusCode, getErr.Error(), synErr)
		return
	}

	// Filter out internal fields and convert ID to appropriate type for response
	filteredEntity := s.filterInternalFieldsWithIDConversion(entity)
	s.respondWithJSON(w, http.StatusOK, filteredEntity)
}

// handleUpdateEntity updates a specific entity
func (s *Server) handleUpdateEntity(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	rawID := vars["id"]
	entityType := vars["type"]

	var updateData struct {
		Fields map[string]interface{} `json:"fields"`
	}

	if err := json.NewDecoder(r.Body).Decode(&updateData); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid request payload",
			errors.NewError(errors.ErrCodeMalformedData, "Failed to decode update data"))
		return
	}
	defer r.Body.Close()

	// Normalize the ID based on entity type's ID generator
	normalizedID, err := s.normalizeEntityID(entityType, rawID)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			errors.NewError(errors.ErrCodeInvalidID, err.Error()))
		return
	}

	// Add debugging information in development mode
	if s.config.DebugMode {
		s.logger.WithFields(logrus.Fields{
			"entityType":   entityType,
			"rawID":        rawID,
			"normalizedID": normalizedID,
		}).Debug("Updating entity")
	}

	// Use the new type-safe Update method
	if err := s.engine.Update(entityType, normalizedID, updateData.Fields); err != nil {
		synErr := datastore.ConvertToSyncopateError(err)
		statusCode := http.StatusBadRequest

		// Check for specific error types
		if errors.IsErrorCode(synErr, errors.ErrCodeUniqueConstraint) {
			statusCode = http.StatusConflict
		} else if errors.IsErrorCode(synErr, errors.ErrCodeEntityNotFound) {
			statusCode = http.StatusNotFound
		}

		s.respondWithError(w, statusCode, err.Error(), synErr)
		return
	}

	// Get entity definition to determine how to format the response ID
	def, err := s.engine.GetEntityDefinition(entityType)
	if err == nil && def.IDGenerator == common.IDTypeAutoIncrement {
		// For auto-increment, convert back to int for the response
		if intID, err := strconv.Atoi(rawID); err == nil {
			s.respondWithJSON(w, http.StatusOK, map[string]interface{}{
				"message": "Entity updated successfully",
				"id":      intID,
			})
			return
		}
	}

	// For other types or if conversion fails, use the raw ID
	s.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Entity updated successfully",
		"id":      rawID,
	})
}

// handleDeleteEntity deletes a specific entity
func (s *Server) handleDeleteEntity(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	rawID := vars["id"]
	entityType := vars["type"]

	// Get entity definition to determine ID type
	def, err := s.engine.GetEntityDefinition(entityType)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			datastore.ConvertToSyncopateError(err))
		return
	}

	// Special handling for auto-increment IDs
	if def.IDGenerator == common.IDTypeAutoIncrement {
		// IMPORTANT: For auto-increment, we need to make sure we're using
		// exactly the same string format as when it was stored
		// Try multiple formats to find the entity
		formats := []string{rawID}

		// Only try conversion if it's a valid number
		if _, err := strconv.Atoi(rawID); err == nil {
			// Try multiple formatting approaches
			id, _ := strconv.ParseUint(rawID, 10, 64)
			formats = append(formats, strconv.FormatUint(id, 10))

			id2, _ := strconv.ParseInt(rawID, 10, 64)
			formats = append(formats, strconv.FormatInt(id2, 10))

			intID, _ := strconv.Atoi(rawID)
			formats = append(formats, strconv.Itoa(intID))
		}

		// Try each format until we find the entity
		entityFound := false
		for _, format := range formats {
			// Try to get the entity with this format
			if _, err := s.engine.Get(format); err == nil {
				// Found it! Use this format for deletion
				rawID = format
				entityFound = true
				break
			}
		}

		if !entityFound {
			// If no format worked, use the direct approach - check the debug endpoint for clues
			engine, ok := s.engine.(*datastore.Engine)
			if ok {
				engine.DebugInspectEntities(func(entities map[string]common.Entity) {
					// Look for an entity with matching ID and type
					for _, entity := range entities {
						if entity.Type == entityType && entity.ID == rawID {
							entityFound = true
							break
						}
					}
				})
			}

			if !entityFound {
				s.respondWithError(w, http.StatusNotFound, "Entity not found with any ID format",
					errors.NewError(errors.ErrCodeEntityNotFound,
						fmt.Sprintf("Entity with ID '%s' and type '%s' not found", rawID, entityType)))
				return
			}
		}
	}

	// Normalize the ID to ensure it is in the correct format for internal use
	// For auto_increment IDs, this will ensure it's the simple numeric string.
	// For UUIDs, it standardizes casing etc.
	normalizedID, err := s.normalizeEntityID(entityType, rawID)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			errors.NewError(errors.ErrCodeInvalidID, err.Error()))
		return
	}

	// At this point, we should have the right format
	if s.config.DebugMode {
		s.logger.WithFields(logrus.Fields{
			"entityType":   entityType,   // This is from the URL, e.g., "posts"
			"rawID":        rawID,        // e.g., "1"
			"normalizedID": normalizedID, // e.g., "1"
		}).Debug("Deleting entity")
	}

	if err := s.engine.Delete(entityType, normalizedID); err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			datastore.ConvertToSyncopateError(err))
		return
	}

	// Format the response ID based on entity type's ID generator
	var responseID interface{} = rawID

	// If it's an auto_increment type, convert to int for API response
	if def.IDGenerator == common.IDTypeAutoIncrement {
		if intID, err := strconv.Atoi(rawID); err == nil {
			responseID = intID
		}
	}

	s.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"message": "Entity deleted successfully",
		"id":      responseID,
	})
}

// handleQuery handles complex query requests
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var queryOpts datastore.QueryOptions
	if err := json.NewDecoder(r.Body).Decode(&queryOpts); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid request payload",
			errors.NewError(errors.ErrCodeMalformedData, "Failed to decode query options"))
		return
	}
	defer r.Body.Close()

	response, err := s.queryService.ExecutePaginatedQuery(queryOpts)
	if err != nil {
		synErr := datastore.ConvertToSyncopateError(err)
		s.respondWithError(w, http.StatusBadRequest, err.Error(), synErr)
		return
	}

	// Get the entity definition to determine ID type
	def, err := s.engine.GetEntityDefinition(queryOpts.EntityType)
	if err != nil {
		synErr := datastore.ConvertToSyncopateError(err)
		s.respondWithError(w, http.StatusBadRequest, err.Error(), synErr)
		return
	}

	// Filter internal fields from response data, ensure all fields are included, and convert IDs
	filteredData := make([]interface{}, len(response.Data))
	for i, entity := range response.Data {
		// Create a filtered copy of the entity
		filteredEntity := common.Entity{
			ID:     entity.ID,
			Type:   entity.Type,
			Fields: make(map[string]interface{}),
		}

		// Copy non-internal fields (those not starting with underscore)
		for name, value := range entity.Fields {
			if !strings.HasPrefix(name, "_") {
				filteredEntity.Fields[name] = value
			}
		}

		// Ensure all fields from definition are included
		completeEntity := s.includeAllDefinedFields(filteredEntity, def)

		// Then convert to representation with proper ID type
		filteredData[i] = common.ConvertToRepresentation(completeEntity, def.IDGenerator)
	}

	// Create a new response with the filtered and converted data
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

// parseQueryParams extracts common query parameters
func (s *Server) parseQueryParams(r *http.Request) (limit int, offset int, orderBy string, orderDesc bool) {
	// Default values
	limit = 100
	offset = 0
	orderDesc = false

	// Parse limit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	// Parse offset
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
			offset = o
		}
	}

	// Parse orderBy
	orderBy = r.URL.Query().Get("orderBy")

	// Parse orderDesc
	orderDesc = r.URL.Query().Get("orderDesc") == "true"

	return
}

// filterInternalFields removes internal fields from entity data based on debug settings
func (s *Server) filterInternalFields(entity common.Entity) common.Entity {
	// Check if debug mode is enabled via environment variable
	debugMode := os.Getenv("SYNCOPATE_DEBUG") == "true"

	// If debug mode is enabled, return the full entity with internal fields
	if debugMode {
		return entity
	}

	// Create a filtered copy of the entity
	filteredEntity := common.Entity{
		ID:     entity.ID,
		Type:   entity.Type,
		Fields: make(map[string]interface{}),
	}

	// Copy only non-internal fields (those not starting with underscore)
	for name, value := range entity.Fields {
		if !strings.HasPrefix(name, "_") {
			filteredEntity.Fields[name] = value
		}
	}

	return filteredEntity
}

// filterInternalFieldsWithIDConversion removes internal fields from entity data
// and converts the ID to the appropriate type based on the entity's ID generator
func (s *Server) filterInternalFieldsWithIDConversion(entity common.Entity) interface{} {
	// Get entity definition to check the ID generator type
	def, err := s.engine.GetEntityDefinition(entity.Type)
	if err != nil {
		// If we can't get the definition, use string ID (fallback)
		return s.filterInternalFields(entity)
	}

	// First, filter out internal fields
	filteredEntity := s.filterInternalFields(entity)

	// Then, ensure all fields from definition are included
	completeEntity := s.includeAllDefinedFields(filteredEntity, def)

	// Convert to representation with proper ID type
	return common.ConvertToRepresentation(completeEntity, def.IDGenerator)
}

// determineEnvironment tries to detect the current deployment environment
func determineEnvironment() string {
	// Check for common environment variables
	if env := os.Getenv("APP_ENV"); env != "" {
		return env
	}

	if env := os.Getenv("ENV"); env != "" {
		return env
	}

	// Check for debug mode
	if settings.Config.Debug {
		return "development"
	}

	// Default to production
	return "production"
}

func (s *Server) handleUpdateEntityType(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	// First check if the entity type exists
	originalDef, err := s.engine.GetEntityDefinition(name)
	if err != nil {
		s.respondWithError(w, http.StatusNotFound, fmt.Sprintf("Entity type '%s' not found", name),
			errors.NewError(errors.ErrCodeEntityTypeNotFound, fmt.Sprintf("Entity type '%s' not found", name)))
		return
	}

	// Parse the updated definition
	var updatedDef common.EntityDefinition
	if err := json.NewDecoder(r.Body).Decode(&updatedDef); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid request payload",
			errors.NewError(errors.ErrCodeMalformedData, "Failed to decode entity definition"))
		return
	}
	defer r.Body.Close()

	// Ensure the name in the payload matches the URL
	if updatedDef.Name != name {
		s.respondWithError(w, http.StatusBadRequest,
			"Entity type name in payload doesn't match URL parameter",
			errors.NewError(errors.ErrCodeInvalidEntityType, "Entity type name in payload doesn't match URL parameter"))
		return
	}

	// Prevent changing the ID generator - this is a design decision to avoid
	// complex ID migration issues
	if updatedDef.IDGenerator != "" && updatedDef.IDGenerator != originalDef.IDGenerator {
		s.respondWithError(w, http.StatusBadRequest,
			"Cannot change the ID generator after entity type creation",
			errors.NewError(errors.ErrCodeIDGeneratorChange, "Cannot change the ID generator after entity type creation"))
		return
	}

	// Include the original ID generator if not specified
	if updatedDef.IDGenerator == "" {
		updatedDef.IDGenerator = originalDef.IDGenerator
	}

	// Check for uniqueness constraint changes
	oldUniqueFields := make(map[string]bool)
	for _, field := range originalDef.Fields {
		if field.Unique {
			oldUniqueFields[field.Name] = true
		}
	}

	newUniqueFields := make(map[string]bool)
	for _, field := range updatedDef.Fields {
		if field.Unique {
			newUniqueFields[field.Name] = true
		}
	}

	// Update the entity type
	if err := s.engine.UpdateEntityType(updatedDef); err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			datastore.ConvertToSyncopateError(err))
		return
	}

	// Get the actual updated definition with any modifications applied
	updatedDef, err = s.engine.GetEntityDefinition(name)
	if err != nil {
		s.respondWithError(w, http.StatusInternalServerError,
			"Entity type updated but could not retrieve it",
			errors.NewError(errors.ErrCodeInternalServer, "Entity type updated but could not retrieve it"))
		return
	}

	// Provide a detailed response with information about the update
	response := map[string]interface{}{
		"message":    "Entity type updated successfully",
		"entityType": updatedDef,
	}

	// If unique constraints were added, mention it in the response
	addedUniqueFields := make([]string, 0)
	for field := range newUniqueFields {
		if !oldUniqueFields[field] {
			addedUniqueFields = append(addedUniqueFields, field)
		}
	}

	if len(addedUniqueFields) > 0 {
		response["uniqueConstraintsAdded"] = addedUniqueFields
	}

	// If unique constraints were removed, mention it in the response
	removedUniqueFields := make([]string, 0)
	for field := range oldUniqueFields {
		if !newUniqueFields[field] {
			removedUniqueFields = append(removedUniqueFields, field)
		}
	}

	if len(removedUniqueFields) > 0 {
		response["uniqueConstraintsRemoved"] = removedUniqueFields
	}

	s.respondWithJSON(w, http.StatusOK, response)
}

// handleDebugSchema provides detailed schema information for debugging purposes
func (s *Server) handleDebugSchema(w http.ResponseWriter, r *http.Request) {
	// Get an entity type from query parameter
	entityType := r.URL.Query().Get("type")

	if entityType == "" {
		// If no specific type requested, show all entity types
		types := s.engine.ListEntityTypes()
		schemas := make(map[string]interface{})

		for _, typeName := range types {
			def, err := s.engine.GetEntityDefinition(typeName)
			if err != nil {
				// Log the error but continue with other types
				s.logger.WithFields(logrus.Fields{
					"entity_type": typeName,
					"error":       err.Error(),
				}).Warn("Failed to get entity definition during debug schema request")
				continue
			}
			schemas[typeName] = def
		}

		s.respondWithJSON(w, http.StatusOK, map[string]interface{}{
			"entity_types": schemas,
		}, true)
		return
	}

	// Get definition for specific entity type
	def, err := s.engine.GetEntityDefinition(entityType)
	if err != nil {
		s.respondWithError(w, http.StatusNotFound,
			fmt.Sprintf("Entity type '%s' not found", entityType),
			errors.NewError(errors.ErrCodeEntityTypeNotFound,
				fmt.Sprintf("Entity type '%s' not found", entityType)))
		return
	}

	// Show detailed schema information
	fieldMap := make(map[string]map[string]interface{})
	for _, field := range def.Fields {
		fieldMap[field.Name] = map[string]interface{}{
			"type":     field.Type,
			"indexed":  field.Indexed,
			"required": field.Required,
			"nullable": field.Nullable,
			"internal": field.Internal,
			"unique":   field.Unique,
		}
	}

	// Get count of entities with this type
	count, err := s.engine.GetEntityCount(entityType)
	if err != nil {
		// Don't fail the request, but log the error
		s.logger.WithFields(logrus.Fields{
			"entity_type": entityType,
			"error":       err.Error(),
		}).Warn("Failed to get entity count during debug schema request")
		count = -1 // Indicate count error
	}

	s.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"entity_type":  def.Name,
		"id_generator": def.IDGenerator,
		"fields":       fieldMap,
		"entity_count": count,
	}, true)
}

// handleCountQuery handles count queries without returning the actual data
func (s *Server) handleCountQuery(w http.ResponseWriter, r *http.Request) {
	var queryOpts datastore.QueryOptions
	if err := json.NewDecoder(r.Body).Decode(&queryOpts); err != nil {
		s.respondWithError(w, http.StatusBadRequest, "Invalid request payload",
			errors.NewError(errors.ErrCodeMalformedData, "Failed to decode query options"))
		return
	}
	defer r.Body.Close()

	// Log the request if in debug mode
	if s.config.DebugMode {
		s.logger.WithFields(logrus.Fields{
			"entityType": queryOpts.EntityType,
			"filters":    len(queryOpts.Filters),
			"joins":      len(queryOpts.Joins),
		}).Debug("Executing count query")
	}

	// Track execution time
	startTime := time.Now()

	// Execute the auto-optimizing count query
	count, err := s.queryService.ExecuteCountQuery(queryOpts)
	if err != nil {
		s.respondWithError(w, http.StatusBadRequest, err.Error(),
			datastore.ConvertToSyncopateError(err))
		return
	}

	// Calculate execution time
	executionTime := time.Since(startTime)

	// Determine query type
	queryType := "simple"
	if len(queryOpts.Joins) > 0 {
		queryType = "join"
	}

	// Create response
	response := CountResponse{
		Count:         count,
		EntityType:    queryOpts.EntityType,
		QueryType:     queryType,
		FiltersCount:  len(queryOpts.Filters),
		JoinsApplied:  len(queryOpts.Joins),
		ExecutionTime: executionTime.String(),
	}

	s.respondWithJSON(w, http.StatusOK, response)
}

// handleErrorCodes returns documentation for all error codes
func (s *Server) handleErrorCodes(w http.ResponseWriter, r *http.Request) {
	// Get query parameters
	codeParam := r.URL.Query().Get("code")
	categoryParam := r.URL.Query().Get("category")
	formatParam := r.URL.Query().Get("format")
	httpStatusParam := r.URL.Query().Get("http_status")

	// Handle specific error code request
	if codeParam != "" {
		// Return details for a specific error code
		if doc, exists := errors.ErrorCodeDocs[errors.ErrorCode(codeParam)]; exists {
			s.respondWithJSON(w, http.StatusOK, doc, true)
			return
		}

		s.respondWithError(w, http.StatusNotFound, "Error code not found",
			errors.NewError(errors.ErrCodeInvalidRequest,
				fmt.Sprintf("Error code '%s' not found", codeParam)))
		return
	}

	// Group error codes by category
	categories := make(map[string][]errors.ErrorCodeDoc)

	for _, doc := range errors.ErrorCodeDocs {
		// Filter by category if specified
		if categoryParam != "" && strings.ToLower(errors.CategoryForErrorCode(doc.Code)) != strings.ToLower(categoryParam) {
			continue
		}

		// Filter by HTTP status if specified
		if httpStatusParam != "" {
			status, err := strconv.Atoi(httpStatusParam)
			if err != nil || doc.HTTPStatus != status {
				continue
			}
		}

		category := errors.CategoryForErrorCode(doc.Code)
		categories[category] = append(categories[category], doc)
	}

	// Sort error codes within each category
	for category := range categories {
		sort.Slice(categories[category], func(i, j int) bool {
			return string(categories[category][i].Code) < string(categories[category][j].Code)
		})
	}

	// Plain text format
	if formatParam == "text" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, "SYNCOPATEDB ERROR CODES\n")
		fmt.Fprintf(w, "=====================\n\n")

		totalCodes := 0

		for category, docs := range categories {
			fmt.Fprintf(w, "%s Errors:\n", category)
			fmt.Fprintf(w, "%s\n\n", strings.Repeat("-", len(category)+8))

			for _, doc := range docs {
				fmt.Fprintf(w, "Code:        %s\n", doc.Code)
				fmt.Fprintf(w, "Name:        %s\n", doc.Name)
				fmt.Fprintf(w, "Description: %s\n", doc.Description)
				fmt.Fprintf(w, "HTTP Status: %d\n", doc.HTTPStatus)
				fmt.Fprintf(w, "Example:     %s\n\n", doc.Example)

				totalCodes++
			}
		}

		fmt.Fprintf(w, "Total Error Codes: %d\n", totalCodes)
		return
	}

	// Get all available categories
	allCategories := make([]string, 0, len(categories))
	for category := range categories {
		allCategories = append(allCategories, category)
	}
	sort.Strings(allCategories)

	// Get all available HTTP status codes
	httpStatuses := make(map[int]string)
	for _, doc := range errors.ErrorCodeDocs {
		if _, exists := httpStatuses[doc.HTTPStatus]; !exists {
			httpStatuses[doc.HTTPStatus] = http.StatusText(doc.HTTPStatus)
		}
	}

	// Build HTTP status list in order
	statusList := make([]map[string]interface{}, 0, len(httpStatuses))
	for code, text := range httpStatuses {
		statusList = append(statusList, map[string]interface{}{
			"code": code,
			"text": text,
		})
	}

	// Sort by status code
	sort.Slice(statusList, func(i, j int) bool {
		return statusList[i]["code"].(int) < statusList[j]["code"].(int)
	})

	// Create response with metadata
	response := map[string]interface{}{
		"total_error_codes": len(errors.ErrorCodeDocs),
		"categories":        categories,
		"available_filters": map[string]interface{}{
			"categories":    allCategories,
			"http_statuses": statusList,
		},
		"usage": map[string]interface{}{
			"all_codes":      "/api/v1/errors",
			"specific_code":  "/api/v1/errors?code=SY001",
			"by_category":    "/api/v1/errors?category=Entity",
			"by_http_status": "/api/v1/errors?http_status=404",
			"plain_text":     "/api/v1/errors?format=text",
		},
	}

	s.respondWithJSON(w, http.StatusOK, response, true)
}

// CountResponse structure for count query responses
type CountResponse struct {
	Count         int    `json:"count"`
	EntityType    string `json:"entityType"`
	QueryType     string `json:"queryType"`
	FiltersCount  int    `json:"filtersCount"`
	JoinsApplied  int    `json:"joinsApplied"`
	ExecutionTime string `json:"executionTime,omitempty"`
}
