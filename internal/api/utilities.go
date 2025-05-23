package api

import (
	"fmt"
	"github.com/phillarmonic/syncopate-db/internal/common"
	"strconv"
	"strings"
)

// normalizeEntityID ensures that an ID is in the correct format for internal storage
// based on the entity type's ID generator
func (s *Server) normalizeEntityID(entityType string, rawID string) (string, error) {
	// Get entity definition to determine ID type
	def, err := s.engine.GetEntityDefinition(entityType)
	if err != nil {
		return "", fmt.Errorf("entity type not found: %w", err)
	}

	switch def.IDGenerator {
	case common.IDTypeAutoIncrement:
		// For auto-increment, ensure it's a valid number
		id, err := strconv.ParseUint(rawID, 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid auto-increment ID format: %w", err)
		}
		// Format exactly as the generator would
		return strconv.FormatUint(id, 10), nil

	case common.IDTypeUUID:
		// For UUID, normalize to lowercase as per RFC 4122
		return strings.ToLower(rawID), nil

	case common.IDTypeCUID:
		// CUIDs should already be properly formatted, but validate format
		if !strings.HasPrefix(rawID, "c") {
			return "", fmt.Errorf("invalid CUID format: must start with 'c'")
		}
		return rawID, nil

	case common.IDTypeCustom:
		// For custom IDs, use as-is
		return rawID, nil

	default:
		// Unknown ID type, use as-is but log a warning
		s.logger.Warnf("Unknown ID generator type: %s, using raw ID", def.IDGenerator)
		return rawID, nil
	}
}

// estimateCompressionRatio estimates the compression ratio for a sample payload
func (s *Server) estimateCompressionRatio(data []byte) float64 {
	if s.compressor == nil {
		return 1.0 // No compression
	}

	// Compress the data
	compressed := s.compressor.EncodeAll(data, nil)

	// Calculate ratio (original size / compressed size)
	if len(compressed) == 0 {
		return 1.0
	}

	return float64(len(data)) / float64(len(compressed))
}

// formatCompressionRatio formats a compression ratio as a percentage string
func formatCompressionRatio(ratio float64) string {
	// Subtract 1 and convert to percentage (e.g., 2.5x becomes "60% smaller")
	if ratio <= 1.0 {
		return "0% (no compression)"
	}

	reductionPercent := (1.0 - (1.0 / ratio)) * 100
	return fmt.Sprintf("%.1f%% smaller (%.1fx)", reductionPercent, ratio)
}

// includeAllDefinedFields adds all fields from the entity definition
// to the entity response, setting undefined fields to null
func (s *Server) includeAllDefinedFields(entity common.Entity, def common.EntityDefinition) common.Entity {
	// Create a copy of the entity to avoid modifying the original
	result := common.Entity{
		ID:     entity.ID,
		Type:   entity.Type,
		Fields: make(map[string]interface{}),
	}

	// Copy existing fields
	for key, value := range entity.Fields {
		result.Fields[key] = value
	}

	// Add all fields from definition that aren't already present
	// and aren't internal
	for _, fieldDef := range def.Fields {
		// Skip internal fields
		if fieldDef.Internal || strings.HasPrefix(fieldDef.Name, "_") {
			continue
		}

		// IMPORTANT: Skip fields named "id" to avoid duplicating the entity ID
		// which is already exposed at the top level and to prevent update issues
		if fieldDef.Name == "id" {
			continue
		}

		// If field is not in the entity, add it with null value
		if _, exists := result.Fields[fieldDef.Name]; !exists {
			result.Fields[fieldDef.Name] = nil
		}
	}

	return result
}
