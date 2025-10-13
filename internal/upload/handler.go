package upload

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RedHatInsights/insights-ros-ingress/internal/config"
	"github.com/RedHatInsights/insights-ros-ingress/internal/health"
	"github.com/RedHatInsights/insights-ros-ingress/internal/logger"
	"github.com/RedHatInsights/insights-ros-ingress/internal/messaging"
	"github.com/RedHatInsights/insights-ros-ingress/internal/storage"
	"github.com/google/uuid"
	"github.com/redhatinsights/platform-go-middlewares/v2/identity"
	"github.com/sirupsen/logrus"
)

// Handler handles HCCM upload requests
type Handler struct {
	config           *config.Config
	storageClient    *storage.Client
	messagingClient  *messaging.Producer
	payloadExtractor *PayloadExtractor
	logger           *logrus.Logger
}

// UploadResponse represents the response returned to clients
type UploadResponse struct {
	RequestID string     `json:"request_id"`
	Upload    UploadData `json:"upload,omitempty"`
}

// UploadData represents upload metadata in response
type UploadData struct {
	Account string `json:"account_number,omitempty"`
	OrgID   string `json:"org_id,omitempty"`
}

// NewHandler creates a new upload handler
// Authentication is expected to be handled by middleware that stores user info in request context
func NewHandler(cfg *config.Config, storageClient *storage.Client, messagingClient *messaging.Producer, log *logrus.Logger) *Handler {
	return &Handler{
		config:           cfg,
		storageClient:    storageClient,
		messagingClient:  messagingClient,
		payloadExtractor: NewPayloadExtractor(cfg.Upload.TempDir, log),
		logger:           log,
	}
}

// HandleUpload handles the main upload endpoint
func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	requestID := h.generateRequestID()

	// Create request logger
	requestLogger := logger.WithUploadContext(h.logger, requestID, "", "")

	defer func() {
		health.HTTPRequestDuration.WithLabelValues(r.Method, "/upload").Observe(time.Since(start).Seconds())
	}()

	requestLogger.WithFields(logrus.Fields{
		"method":         r.Method,
		"user_agent":     r.Header.Get("User-Agent"),
		"content_length": r.ContentLength,
	}).Info("Received upload request")

	// Validate request method
	if r.Method != http.MethodPost {
		h.respondError(w, http.StatusMethodNotAllowed, "Method not allowed", requestLogger)
		return
	}

	// Handle test requests
	if h.isTestRequest(r) {
		h.handleTestRequest(w, r, requestID, requestLogger)
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(h.config.Upload.MaxMemory); err != nil {
		h.respondError(w, http.StatusBadRequest, "Failed to parse multipart form", requestLogger)
		return
	}

	// Extract identity from header and get JWT token
	// When auth is disabled, identity will be nil and jwtToken will be empty
	identity, err := h.extractIdentity(r)
	if err != nil && h.config.Auth.Enabled {
		h.respondError(w, http.StatusUnauthorized, "Invalid or missing identity", requestLogger)
		return
	}

	// Update logger with identity context
	if identity != nil {
		requestLogger = logger.WithUploadContext(h.logger, requestID, identity.AccountNumber, identity.OrgID)
	}

	// Extract JWT token for downstream processing
	// When auth is disabled, this will be empty string which is acceptable
	jwtToken := h.extractJWTToken(r)

	// Get file from multipart form
	file, fileHeader, err := h.getFileFromRequest(r)
	if err != nil {
		h.respondError(w, http.StatusBadRequest, "File not found in request", requestLogger)
		return
	}
	defer func() {
		if err := file.Close(); err != nil {
			requestLogger.WithError(err).Warn("Failed to close uploaded file")
		}
	}()

	// Validate content type
	contentType := fileHeader.Header.Get("Content-Type")
	if !h.isValidContentType(contentType) {
		h.respondError(w, http.StatusUnsupportedMediaType, "Invalid content type", requestLogger)
		return
	}

	// Validate file size
	if fileHeader.Size > h.config.Upload.MaxUploadSize {
		h.respondError(w, http.StatusRequestEntityTooLarge, "File too large", requestLogger)
		return
	}

	requestLogger.WithFields(logrus.Fields{
		"content_type": contentType,
		"file_size":    fileHeader.Size,
	}).Info("Processing upload")

	// Record upload metrics
	health.UploadsTotal.WithLabelValues("received", contentType).Inc()
	health.UploadSizeBytes.WithLabelValues(contentType).Observe(float64(fileHeader.Size))

	// Process the upload
	if err := h.processUpload(r.Context(), file, requestID, identity, jwtToken, requestLogger); err != nil {
		health.UploadsTotal.WithLabelValues("error", contentType).Inc()
		h.respondError(w, http.StatusInternalServerError, "Failed to process upload", requestLogger)
		requestLogger.WithError(err).Error("Upload processing failed")
		return
	}

	health.UploadsTotal.WithLabelValues("success", contentType).Inc()

	// Send success response
	response := UploadResponse{
		RequestID: requestID,
	}

	if identity != nil {
		response.Upload = UploadData{
			Account: identity.AccountNumber,
			OrgID:   identity.OrgID,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		requestLogger.WithError(err).Error("Failed to encode response")
	}

	requestLogger.Info("Upload processed successfully")
}

// processUpload handles the core upload processing logic
func (h *Handler) processUpload(ctx context.Context, file io.Reader, requestID string, identity *identity.Identity, jwtToken string, logger *logrus.Entry) error {
	// Extract payload
	extractedPayload, err := h.payloadExtractor.ExtractPayload(file, requestID)
	if err != nil {
		return fmt.Errorf("failed to extract payload: %w", err)
	}
	defer func() {
		if err := extractedPayload.Cleanup(); err != nil {
			logger.WithError(err).Warn("Failed to cleanup extracted payload")
		}
	}()

	// Validate that we have ROS files to process
	if len(extractedPayload.ROSFiles) == 0 {
		return fmt.Errorf("no ROS files found in payload")
	}

	logger.WithField("ros_files_count", len(extractedPayload.ROSFiles)).Info("Found ROS files in payload")

	// Upload ROS files to storage and collect URLs
	var uploadedFiles []string
	var objectKeys []string

	for fileName, filePath := range extractedPayload.ROSFiles {
		// Open ROS file
		rosFile, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("failed to open ROS file %s: %w", fileName, err)
		}

		// Get file info
		fileInfo, err := rosFile.Stat()
		if err != nil {
			if closeErr := rosFile.Close(); closeErr != nil {
				logger.WithError(closeErr).Warn("Failed to close ROS file after stat error")
			}
			return fmt.Errorf("failed to stat ROS file %s: %w", fileName, err)
		}

		// Generate storage path
		schema := h.getSchemaName(identity)
		sourceID := extractedPayload.Manifest.ClusterID
		date := extractedPayload.Manifest.Date.Format("2006-01-02")
		uploadKey := h.storageClient.GenerateUploadPath(schema, sourceID, date, fileName)

		// Prepare upload request
		uploadReq := &storage.UploadRequest{
			Key:         uploadKey,
			Data:        rosFile,
			Size:        fileInfo.Size(),
			ContentType: "text/csv",
			Metadata: map[string]string{
				"ManifestId":      extractedPayload.Manifest.UUID,
				"RequestId":       requestID,
				"ClusterUuid":     extractedPayload.Manifest.ClusterID,
				"OperatorVersion": extractedPayload.Manifest.OperatorVersion,
			},
		}

		// Upload to storage
		uploadResult, err := h.storageClient.Upload(ctx, uploadReq)
		if closeErr := rosFile.Close(); closeErr != nil {
			logger.WithError(closeErr).Warn("Failed to close ROS file after upload")
		}

		if err != nil {
			return fmt.Errorf("failed to upload ROS file %s: %w", fileName, err)
		}

		uploadedFiles = append(uploadedFiles, uploadResult.PresignedURL)
		objectKeys = append(objectKeys, uploadResult.Key)

		logger.WithFields(logrus.Fields{
			"file_name": fileName,
			"key":       uploadResult.Key,
			"size":      uploadResult.Size,
		}).Info("Successfully uploaded ROS file")
	}

	// Send ROS event message
	// Use the JWT token from Keycloak for downstream authentication
	// If auth is disabled, jwtToken will be empty string
	rosMessage := &messaging.ROSMessage{
		RequestID:   requestID,
		B64Identity: jwtToken,
		Metadata: messaging.ROSMetadata{
			Account:         h.getAccountID(identity),
			OrgID:           h.getOrgID(identity),
			SourceID:        extractedPayload.Manifest.ClusterID, // Using cluster ID as source ID
			ProviderUUID:    extractedPayload.Manifest.ClusterID, // Using cluster ID as provider UUID
			ClusterUUID:     extractedPayload.Manifest.ClusterID,
			ClusterAlias:    h.getClusterAlias(extractedPayload.Manifest),
			OperatorVersion: extractedPayload.Manifest.OperatorVersion,
		},
		Files:      uploadedFiles,
		ObjectKeys: objectKeys,
	}

	if err := h.messagingClient.SendROSEvent(ctx, rosMessage); err != nil {
		return fmt.Errorf("failed to send ROS event: %w", err)
	}

	logger.WithFields(logrus.Fields{
		"topic":          h.config.Kafka.Topic,
		"uploaded_files": len(uploadedFiles),
	}).Info("Successfully sent ROS event message")

	// Send validation confirmation
	if err := h.messagingClient.SendValidationMessage(ctx, requestID, "success"); err != nil {
		// Log error but don't fail the request
		logger.WithError(err).Warn("Failed to send validation message")
	}

	return nil
}

// Helper methods

func (h *Handler) generateRequestID() string {
	return uuid.New().String()
}

// getClusterAlias returns the cluster alias from manifest, falling back to cluster ID
// This matches koku's behavior: prefer explicit alias, fallback to cluster ID
func (h *Handler) getClusterAlias(manifest *Manifest) string {
	if manifest.ClusterAlias != "" {
		return manifest.ClusterAlias
	}
	// Fallback to cluster ID if no explicit alias is provided
	// This matches koku's get_cluster_alias() behavior
	return manifest.ClusterID
}

func (h *Handler) isTestRequest(r *http.Request) bool {
	// Check form data for test request
	if r.FormValue("test") == "test" {
		return true
	}

	// Check Content-Type for JSON test requests
	if r.Header.Get("Content-Type") == "application/json" {
		// This would need to read the body, but we'll keep it simple for now
		return false
	}

	return false
}

func (h *Handler) handleTestRequest(w http.ResponseWriter, _ *http.Request, requestID string, logger *logrus.Entry) {
	logger.Info("Handling test request")

	response := UploadResponse{
		RequestID: requestID,
		Upload: UploadData{
			Account: "test-account",
			OrgID:   "test-org",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.WithError(err).Error("Failed to encode test response")
	}
}

func (h *Handler) extractIdentity(r *http.Request) (*identity.Identity, error) {
	if !h.config.Auth.Enabled {
		return nil, nil
	}

	// Check if request is authenticated by Envoy/Authorino sidecar
	// The sidecar forwards these headers after validating the Keycloak JWT:
	// - X-ROS-Authenticated: "true"
	// - X-ROS-User-ID: user ID from JWT sub claim
	// - X-Bearer-Token: original JWT token
	authenticated := r.Header.Get("X-ROS-Authenticated")
	if authenticated != "true" {
		return nil, fmt.Errorf("request not authenticated by sidecar - X-ROS-Authenticated header missing or invalid")
	}

	// Get the JWT token from headers
	bearerToken := h.extractJWTToken(r)
	if bearerToken == "" {
		return nil, fmt.Errorf("no JWT token found in X-Bearer-Token or Authorization header")
	}

	// Parse JWT token to extract claims (org_id, account, etc.)
	claims, err := h.parseJWTClaims(bearerToken)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	h.logger.WithFields(logrus.Fields{
		"user_id":  claims["sub"],
		"org_id":   claims["org_id"],
		"account":  claims["account_number"],
		"username": claims["preferred_username"],
	}).Debug("Extracted identity from JWT token")

	// Create identity from JWT claims
	return h.createIdentityFromJWT(claims), nil
}

// extractJWTToken extracts the JWT token from request headers
// Tries X-Bearer-Token first (forwarded by sidecar), then Authorization header (fallback)
func (h *Handler) extractJWTToken(r *http.Request) string {
	// Try X-Bearer-Token first (forwarded by Envoy/Authorino sidecar)
	bearerToken := r.Header.Get("X-Bearer-Token")
	if bearerToken != "" {
		return bearerToken
	}

	// Fallback to Authorization header
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	return ""
}

// parseJWTClaims parses JWT token and extracts claims without validation
// Validation is already done by the Envoy/Authorino sidecar
func (h *Handler) parseJWTClaims(tokenString string) (map[string]interface{}, error) {
	// Split the token into parts
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT token format - expected 3 parts, got %d", len(parts))
	}

	// Decode the payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	// Parse JSON claims
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JWT claims: %w", err)
	}

	return claims, nil
}

// createIdentityFromJWT creates an identity from Keycloak JWT claims
func (h *Handler) createIdentityFromJWT(claims map[string]interface{}) *identity.Identity {
	// Extract standard JWT claims
	username := h.getStringClaim(claims, "preferred_username", "sub")
	email := h.getStringClaim(claims, "email", "")
	firstName := h.getStringClaim(claims, "given_name", "")
	lastName := h.getStringClaim(claims, "family_name", "")

	// Extract Red Hat specific claims
	orgID := h.getStringClaim(claims, "org_id", "organization_id", "tenant_id")
	accountNumber := h.getStringClaim(claims, "account_number", "account_id", "account")

	// Use defaults if not provided
	if orgID == "" {
		orgID = "1" // Default org ID
	}
	if accountNumber == "" {
		accountNumber = "1" // Default account number
	}

	return &identity.Identity{
		AccountNumber: accountNumber,
		OrgID:         orgID,
		Type:          "User",
		AuthType:      "jwt-keycloak",
		User: &identity.User{
			Username:  username,
			Email:     email,
			FirstName: firstName,
			LastName:  lastName,
			Active:    true,
			OrgAdmin:  false,
			Internal:  false,
			Locale:    "en_US",
		},
		Internal: identity.Internal{
			OrgID: orgID,
		},
	}
}

// Helper methods to extract information from JWT claims

func (h *Handler) getStringClaim(claims map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, exists := claims[key]; exists {
			if strValue, ok := value.(string); ok && strValue != "" {
				return strValue
			}
		}
	}
	return ""
}

func (h *Handler) getFileFromRequest(r *http.Request) (io.ReadCloser, *multipart.FileHeader, error) {
	// Try "file" field first, then "upload" field
	file, fileHeader, err := r.FormFile("file")
	if err == nil {
		return file, fileHeader, nil
	}

	file, fileHeader, err = r.FormFile("upload")
	if err == nil {
		return file, fileHeader, nil
	}

	return nil, nil, fmt.Errorf("no file found in request")
}

func (h *Handler) isValidContentType(contentType string) bool {
	for _, allowedType := range h.config.Upload.AllowedTypes {
		if contentType == allowedType {
			return true
		}
	}

	// Also check for gzip patterns
	gzipPattern := regexp.MustCompile(`application/(x-gzip|gzip)(; charset=binary)?`)
	if gzipPattern.MatchString(contentType) {
		return true
	}

	// Check for vnd.redhat patterns
	vndPattern := regexp.MustCompile(`application/vnd\.redhat\.([a-z0-9-]+)\.([a-z0-9-]+).*`)
	return vndPattern.MatchString(contentType)
}

func (h *Handler) getSchemaName(identity *identity.Identity) string {
	if identity != nil && identity.OrgID != "" {
		return fmt.Sprintf("org_%s", identity.OrgID)
	}
	return "default"
}

func (h *Handler) getAccountID(identity *identity.Identity) string {
	if identity != nil {
		return identity.AccountNumber
	}
	// Default fallback when auth is disabled - maintains backwards compatibility
	return "1"
}

func (h *Handler) getOrgID(identity *identity.Identity) string {
	if identity != nil {
		if identity.OrgID == "" {
			return identity.Internal.OrgID
		}
		return identity.OrgID
	}
	// Default fallback when auth is disabled - maintains backwards compatibility
	return "1"
}

func (h *Handler) respondError(w http.ResponseWriter, statusCode int, message string, logger *logrus.Entry) {
	health.HTTPRequestsTotal.WithLabelValues("POST", "/upload", strconv.Itoa(statusCode)).Inc()

	logger.WithFields(logrus.Fields{
		"status_code": statusCode,
		"error":       message,
	}).Warn("Request failed")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	errorResponse := map[string]string{
		"error": message,
	}
	if err := json.NewEncoder(w).Encode(errorResponse); err != nil {
		logger.WithError(err).Error("Failed to encode error response")
	}
}
