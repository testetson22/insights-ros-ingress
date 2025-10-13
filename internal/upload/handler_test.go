package upload

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/RedHatInsights/insights-ros-ingress/internal/config"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("Handler JWT Authentication", func() {
	var (
		handler *Handler
		logger  *logrus.Logger
	)

	BeforeEach(func() {
		logger = logrus.New()
		logger.SetLevel(logrus.ErrorLevel) // Suppress logs during tests
	})

	// Helper function to create a JWT token for testing
	createTestJWT := func(claims map[string]interface{}) string {
		// Create JWT header
		header := map[string]interface{}{
			"alg": "RS256",
			"typ": "JWT",
		}
		headerJSON, _ := json.Marshal(header)
		headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

		// Create JWT payload
		payloadJSON, _ := json.Marshal(claims)
		payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

		// Create fake signature (not validated since sidecar already did it)
		signature := base64.RawURLEncoding.EncodeToString([]byte("fake-signature"))

		return headerB64 + "." + payloadB64 + "." + signature
	}

	Describe("extractIdentity", func() {
		Context("when auth is disabled", func() {
			BeforeEach(func() {
				cfg := &config.Config{
					Auth: config.AuthConfig{
						Enabled: false,
					},
				}
				handler = NewHandler(cfg, nil, nil, logger)
			})

			It("should return nil without error", func() {
				req, _ := http.NewRequest("POST", "/upload", nil)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(BeNil())
			})
		})

		Context("when auth is enabled", func() {
			BeforeEach(func() {
				cfg := &config.Config{
					Auth: config.AuthConfig{
						Enabled: true,
					},
				}
				handler = NewHandler(cfg, nil, nil, logger)
			})

			Context("with valid JWT token", func() {
				It("should extract identity successfully", func() {
					claims := map[string]interface{}{
						"sub":                "user-123",
						"preferred_username": "john.doe",
						"email":              "john@example.com",
						"given_name":         "John",
						"family_name":        "Doe",
						"org_id":             "456",
						"account_number":     "789",
					}
					token := createTestJWT(claims)

					req, _ := http.NewRequest("POST", "/upload", nil)
					req.Header.Set("X-ROS-Authenticated", "true")
					req.Header.Set("X-Bearer-Token", token)

					result, err := handler.extractIdentity(req)

					Expect(err).ToNot(HaveOccurred())
					Expect(result).ToNot(BeNil())
					Expect(result.AccountNumber).To(Equal("789"))
					Expect(result.OrgID).To(Equal("456"))
					Expect(result.Type).To(Equal("User"))
					Expect(result.AuthType).To(Equal("jwt-keycloak"))
					Expect(result.User.Username).To(Equal("john.doe"))
					Expect(result.User.Email).To(Equal("john@example.com"))
					Expect(result.User.FirstName).To(Equal("John"))
					Expect(result.User.LastName).To(Equal("Doe"))
					Expect(result.User.OrgAdmin).To(BeFalse())
					Expect(result.User.Internal).To(BeFalse())
					Expect(result.Internal.OrgID).To(Equal("456"))
				})

				It("should use defaults when claims are missing", func() {
					claims := map[string]interface{}{
						"sub": "user-123",
					}
					token := createTestJWT(claims)

					req, _ := http.NewRequest("POST", "/upload", nil)
					req.Header.Set("X-ROS-Authenticated", "true")
					req.Header.Set("X-Bearer-Token", token)

					result, err := handler.extractIdentity(req)

					Expect(err).ToNot(HaveOccurred())
					Expect(result).ToNot(BeNil())
					Expect(result.AccountNumber).To(Equal("1")) // default
					Expect(result.OrgID).To(Equal("1"))         // default
					Expect(result.User.Username).To(Equal("user-123"))
				})

				It("should work with Authorization header as fallback", func() {
					claims := map[string]interface{}{
						"sub":            "user-123",
						"org_id":         "999",
						"account_number": "888",
					}
					token := createTestJWT(claims)

					req, _ := http.NewRequest("POST", "/upload", nil)
					req.Header.Set("X-ROS-Authenticated", "true")
					req.Header.Set("Authorization", "Bearer "+token)

					result, err := handler.extractIdentity(req)

					Expect(err).ToNot(HaveOccurred())
					Expect(result).ToNot(BeNil())
					Expect(result.AccountNumber).To(Equal("888"))
					Expect(result.OrgID).To(Equal("999"))
				})
			})

			Context("when X-ROS-Authenticated header is missing", func() {
				It("should return error", func() {
					req, _ := http.NewRequest("POST", "/upload", nil)

					result, err := handler.extractIdentity(req)

					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("not authenticated by sidecar"))
					Expect(result).To(BeNil())
				})
			})

			Context("when JWT token is missing", func() {
				It("should return error", func() {
					req, _ := http.NewRequest("POST", "/upload", nil)
					req.Header.Set("X-ROS-Authenticated", "true")

					result, err := handler.extractIdentity(req)

					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("no JWT token found"))
					Expect(result).To(BeNil())
				})
			})

			Context("with malformed JWT token", func() {
				It("should return error", func() {
					req, _ := http.NewRequest("POST", "/upload", nil)
					req.Header.Set("X-ROS-Authenticated", "true")
					req.Header.Set("X-Bearer-Token", "invalid.token")

					result, err := handler.extractIdentity(req)

					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("invalid JWT token format"))
					Expect(result).To(BeNil())
				})
			})
		})
	})

	Describe("extractJWTToken", func() {
		BeforeEach(func() {
			handler = NewHandler(&config.Config{}, nil, nil, logger)
		})

		Context("with X-Bearer-Token header", func() {
			It("should extract token successfully", func() {
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-Bearer-Token", "test-jwt-token")

				result := handler.extractJWTToken(req)

				Expect(result).To(Equal("test-jwt-token"))
			})
		})

		Context("with Authorization header", func() {
			It("should extract token successfully", func() {
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("Authorization", "Bearer another-jwt-token")

				result := handler.extractJWTToken(req)

				Expect(result).To(Equal("another-jwt-token"))
			})
		})

		Context("with both headers present", func() {
			It("should prioritize X-Bearer-Token", func() {
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-Bearer-Token", "priority-token")
				req.Header.Set("Authorization", "Bearer fallback-token")

				result := handler.extractJWTToken(req)

				Expect(result).To(Equal("priority-token"))
			})
		})

		Context("with no token headers", func() {
			It("should return empty string", func() {
				req, _ := http.NewRequest("POST", "/upload", nil)

				result := handler.extractJWTToken(req)

				Expect(result).To(BeEmpty())
			})
		})

		Context("with malformed Authorization header", func() {
			It("should return empty string", func() {
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("Authorization", "InvalidFormat token")

				result := handler.extractJWTToken(req)

				Expect(result).To(BeEmpty())
			})
		})
	})

	Describe("parseJWTClaims", func() {
		BeforeEach(func() {
			handler = NewHandler(&config.Config{}, nil, nil, logger)
		})

		Context("with valid JWT token", func() {
			It("should parse claims successfully", func() {
				claims := map[string]interface{}{
					"sub":                "user-123",
					"org_id":             "456",
					"account_number":     "789",
					"preferred_username": "john.doe",
				}
				token := createTestJWT(claims)

				result, err := handler.parseJWTClaims(token)

				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
				Expect(result["sub"]).To(Equal("user-123"))
				Expect(result["org_id"]).To(Equal("456"))
				Expect(result["account_number"]).To(Equal("789"))
				Expect(result["preferred_username"]).To(Equal("john.doe"))
			})
		})

		Context("with invalid JWT format", func() {
			It("should return error for too few parts", func() {
				_, err := handler.parseJWTClaims("invalid.token")

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid JWT token format"))
			})

			It("should return error for invalid base64", func() {
				_, err := handler.parseJWTClaims("header.!!!invalid-base64!!!.signature")

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to decode JWT payload"))
			})

			It("should return error for invalid JSON", func() {
				invalidJSON := base64.RawURLEncoding.EncodeToString([]byte("{invalid json"))
				token := "header." + invalidJSON + ".signature"

				_, err := handler.parseJWTClaims(token)

				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to unmarshal JWT claims"))
			})
		})
	})

	Describe("createIdentityFromJWT", func() {
		BeforeEach(func() {
			handler = NewHandler(&config.Config{}, nil, nil, logger)
		})

		Context("with complete JWT claims", func() {
			It("should create proper identity", func() {
				claims := map[string]interface{}{
					"sub":                "user-123",
					"preferred_username": "john.doe",
					"email":              "john@example.com",
					"given_name":         "John",
					"family_name":        "Doe",
					"org_id":             "456",
					"account_number":     "789",
				}

				result := handler.createIdentityFromJWT(claims)

				Expect(result).ToNot(BeNil())
				Expect(result.AccountNumber).To(Equal("789"))
				Expect(result.OrgID).To(Equal("456"))
				Expect(result.Type).To(Equal("User"))
				Expect(result.AuthType).To(Equal("jwt-keycloak"))
				Expect(result.User.Username).To(Equal("john.doe"))
				Expect(result.User.Email).To(Equal("john@example.com"))
				Expect(result.User.FirstName).To(Equal("John"))
				Expect(result.User.LastName).To(Equal("Doe"))
				Expect(result.User.Active).To(BeTrue())
				Expect(result.User.OrgAdmin).To(BeFalse())
				Expect(result.User.Internal).To(BeFalse())
				Expect(result.User.Locale).To(Equal("en_US"))
				Expect(result.Internal.OrgID).To(Equal("456"))
			})
		})

		Context("with minimal claims", func() {
			It("should use default values", func() {
				claims := map[string]interface{}{
					"sub": "user-123",
				}

				result := handler.createIdentityFromJWT(claims)

				Expect(result).ToNot(BeNil())
				Expect(result.AccountNumber).To(Equal("1")) // default fallback
				Expect(result.OrgID).To(Equal("1"))         // default fallback
				Expect(result.User.Username).To(Equal("user-123"))
				Expect(result.User.Active).To(BeTrue())
			})
		})

		Context("with alternative claim names", func() {
			It("should extract org_id from organization_id", func() {
				claims := map[string]interface{}{
					"sub":             "user-123",
					"organization_id": "999",
					"account_id":      "888",
				}

				result := handler.createIdentityFromJWT(claims)

				Expect(result.OrgID).To(Equal("999"))
				Expect(result.AccountNumber).To(Equal("888"))
			})

			It("should extract org_id from tenant_id", func() {
				claims := map[string]interface{}{
					"sub":       "user-123",
					"tenant_id": "777",
					"account":   "666",
				}

				result := handler.createIdentityFromJWT(claims)

				Expect(result.OrgID).To(Equal("777"))
				Expect(result.AccountNumber).To(Equal("666"))
			})
		})

		Context("with fallback username from sub", func() {
			It("should use sub when preferred_username is missing", func() {
				claims := map[string]interface{}{
					"sub": "fallback-user",
				}

				result := handler.createIdentityFromJWT(claims)

				Expect(result.User.Username).To(Equal("fallback-user"))
			})
		})
	})

	Describe("getStringClaim", func() {
		BeforeEach(func() {
			handler = NewHandler(&config.Config{}, nil, nil, logger)
		})

		Context("with first key matching", func() {
			It("should return value for first key", func() {
				claims := map[string]interface{}{
					"key1": "value1",
					"key2": "value2",
				}

				result := handler.getStringClaim(claims, "key1", "key2")

				Expect(result).To(Equal("value1"))
			})
		})

		Context("with second key matching", func() {
			It("should return value for second key", func() {
				claims := map[string]interface{}{
					"key2": "value2",
				}

				result := handler.getStringClaim(claims, "key1", "key2")

				Expect(result).To(Equal("value2"))
			})
		})

		Context("with no matching keys", func() {
			It("should return empty string", func() {
				claims := map[string]interface{}{
					"other_key": "other_value",
				}

				result := handler.getStringClaim(claims, "key1", "key2")

				Expect(result).To(BeEmpty())
			})
		})

		Context("with empty value", func() {
			It("should skip empty strings and try next key", func() {
				claims := map[string]interface{}{
					"key1": "",
					"key2": "value2",
				}

				result := handler.getStringClaim(claims, "key1", "key2")

				Expect(result).To(Equal("value2"))
			})
		})

		Context("with non-string value", func() {
			It("should skip non-string values", func() {
				claims := map[string]interface{}{
					"key1": 123,
					"key2": "value2",
				}

				result := handler.getStringClaim(claims, "key1", "key2")

				Expect(result).To(Equal("value2"))
			})
		})
	})

	Describe("Keycloak JWT Format Integration", func() {
		BeforeEach(func() {
			cfg := &config.Config{
				Auth: config.AuthConfig{
					Enabled: true,
				},
			}
			handler = NewHandler(cfg, nil, nil, logger)
		})

		Context("with full realistic Keycloak JWT", func() {
			It("should extract all identity fields correctly", func() {
				// Realistic Keycloak JWT with all standard fields
				claims := map[string]interface{}{
					"exp":                1728000000,
					"iat":                1727913600,
					"auth_time":          1727913600,
					"jti":                "f8c3de3d-1fea-4d7c-a8b0-29f63c4c3454",
					"iss":                "https://keycloak.example.com/auth/realms/kubernetes",
					"aud":                []interface{}{"cost-management-operator", "openshift-oidc"},
					"sub":                "f:12345678-1234-1234-1234-123456789abc:user@example.com",
					"typ":                "Bearer",
					"azp":                "cost-management-operator",
					"session_state":      "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
					"acr":                "1",
					"scope":              "openid profile email",
					"sid":                "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
					"email_verified":     true,
					"name":               "John Doe",
					"preferred_username": "john.doe",
					"given_name":         "John",
					"family_name":        "Doe",
					"email":              "john.doe@example.com",
					"org_id":             "12345",
					"account_number":     "67890",
					"realm_access": map[string]interface{}{
						"roles": []interface{}{"offline_access", "uma_authorization", "default-roles-kubernetes"},
					},
					"resource_access": map[string]interface{}{
						"cost-management-operator": map[string]interface{}{
							"roles": []interface{}{"user", "viewer"},
						},
						"account": map[string]interface{}{
							"roles": []interface{}{"manage-account", "view-profile"},
						},
					},
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
				Expect(result.AccountNumber).To(Equal("67890"))
				Expect(result.OrgID).To(Equal("12345"))
				Expect(result.Type).To(Equal("User"))
				Expect(result.AuthType).To(Equal("jwt-keycloak"))
				Expect(result.User.Username).To(Equal("john.doe"))
				Expect(result.User.Email).To(Equal("john.doe@example.com"))
				Expect(result.User.FirstName).To(Equal("John"))
				Expect(result.User.LastName).To(Equal("Doe"))
				Expect(result.User.Active).To(BeTrue())
				Expect(result.User.OrgAdmin).To(BeFalse()) // No authorization logic
				Expect(result.User.Internal).To(BeFalse()) // No authorization logic
				Expect(result.Internal.OrgID).To(Equal("12345"))
			})
		})

		Context("with Keycloak service account JWT", func() {
			It("should handle service account format", func() {
				claims := map[string]interface{}{
					"exp":                1728000000,
					"iat":                1727913600,
					"jti":                "service-token-id",
					"iss":                "https://keycloak.example.com/auth/realms/kubernetes",
					"aud":                "cost-management-operator",
					"sub":                "service-account-cost-management",
					"typ":                "Bearer",
					"azp":                "cost-management-operator",
					"preferred_username": "service-account-cost-management",
					"org_id":             "service-org-123",
					"account_number":     "service-account-456",
					"clientId":           "cost-management-operator",
					"resource_access": map[string]interface{}{
						"cost-management-operator": map[string]interface{}{
							"roles": []interface{}{"admin", "service"},
						},
					},
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
				Expect(result.AccountNumber).To(Equal("service-account-456"))
				Expect(result.OrgID).To(Equal("service-org-123"))
				Expect(result.User.Username).To(Equal("service-account-cost-management"))
			})
		})

		Context("with Keycloak custom claim mappings", func() {
			It("should handle organization_id as alternative to org_id", func() {
				claims := map[string]interface{}{
					"sub":                "user-123",
					"preferred_username": "test.user",
					"organization_id":    "org-999", // Alternative field name
					"account_number":     "acc-888",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.OrgID).To(Equal("org-999"))
			})

			It("should handle tenant_id as alternative to org_id", func() {
				claims := map[string]interface{}{
					"sub":        "user-123",
					"tenant_id":  "tenant-777", // Another alternative field name
					"account_id": "acc-666",    // Alternative to account_number
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.OrgID).To(Equal("tenant-777"))
				Expect(result.AccountNumber).To(Equal("acc-666"))
			})

			It("should handle account as alternative to account_number", func() {
				claims := map[string]interface{}{
					"sub":     "user-123",
					"org_id":  "org-555",
					"account": "acc-444", // Shortest alternative name
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.AccountNumber).To(Equal("acc-444"))
			})
		})

		Context("with Keycloak email verification states", func() {
			It("should handle verified email", func() {
				claims := map[string]interface{}{
					"sub":                "user-123",
					"email":              "verified@example.com",
					"email_verified":     true,
					"preferred_username": "verified.user",
					"org_id":             "org-123",
					"account_number":     "acc-123",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.User.Email).To(Equal("verified@example.com"))
			})

			It("should handle unverified email", func() {
				claims := map[string]interface{}{
					"sub":                "user-123",
					"email":              "unverified@example.com",
					"email_verified":     false,
					"preferred_username": "unverified.user",
					"org_id":             "org-123",
					"account_number":     "acc-123",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.User.Email).To(Equal("unverified@example.com"))
			})
		})

		Context("with Keycloak federated identity", func() {
			It("should handle federated user ID in sub claim", func() {
				// Keycloak federated identity format: f:<federation-provider>:<external-id>
				claims := map[string]interface{}{
					"sub":                "f:github:12345678",
					"preferred_username": "github-user",
					"email":              "githubuser@example.com",
					"name":               "GitHub User",
					"org_id":             "org-fed-123",
					"account_number":     "acc-fed-456",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
				Expect(result.User.Username).To(Equal("github-user"))
				Expect(result.AccountNumber).To(Equal("acc-fed-456"))
			})
		})

		Context("with Keycloak token with multiple audiences", func() {
			It("should handle array of audiences", func() {
				claims := map[string]interface{}{
					"sub": "user-123",
					"aud": []interface{}{
						"cost-management-operator",
						"openshift-oidc",
						"account",
					},
					"preferred_username": "multi-aud-user",
					"org_id":             "org-multi-123",
					"account_number":     "acc-multi-456",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
			})

			It("should handle single audience string", func() {
				claims := map[string]interface{}{
					"sub":                "user-123",
					"aud":                "cost-management-operator", // String instead of array
					"preferred_username": "single-aud-user",
					"org_id":             "org-single-123",
					"account_number":     "acc-single-456",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result).ToNot(BeNil())
			})
		})

		Context("with Keycloak realm-specific configurations", func() {
			It("should handle kubernetes realm tokens", func() {
				claims := map[string]interface{}{
					"iss":                "https://keycloak.example.com/auth/realms/kubernetes",
					"sub":                "user-k8s-123",
					"preferred_username": "k8s.user",
					"org_id":             "k8s-org-123",
					"account_number":     "k8s-acc-456",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.User.Username).To(Equal("k8s.user"))
			})

			It("should handle openshift realm tokens", func() {
				claims := map[string]interface{}{
					"iss":                "https://keycloak.example.com/auth/realms/openshift",
					"sub":                "user-ocp-123",
					"preferred_username": "ocp.user",
					"org_id":             "ocp-org-123",
					"account_number":     "ocp-acc-456",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.User.Username).To(Equal("ocp.user"))
			})
		})

		Context("with Keycloak token priority of claim fields", func() {
			It("should prioritize org_id over organization_id when both present", func() {
				claims := map[string]interface{}{
					"sub":             "user-123",
					"org_id":          "preferred-org-111",
					"organization_id": "fallback-org-222",
					"account_number":  "acc-123",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.OrgID).To(Equal("preferred-org-111"))
			})

			It("should prioritize account_number over account_id when both present", func() {
				claims := map[string]interface{}{
					"sub":            "user-123",
					"org_id":         "org-123",
					"account_number": "preferred-acc-111",
					"account_id":     "fallback-acc-222",
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.AccountNumber).To(Equal("preferred-acc-111"))
			})
		})

		Context("with Keycloak edge cases", func() {
			It("should handle missing preferred_username by using sub", func() {
				claims := map[string]interface{}{
					"sub":            "fallback-username",
					"org_id":         "org-123",
					"account_number": "acc-123",
					// preferred_username is missing
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.User.Username).To(Equal("fallback-username"))
			})

			It("should handle empty string claims gracefully", func() {
				claims := map[string]interface{}{
					"sub":                "user-123",
					"preferred_username": "", // Empty string
					"email":              "", // Empty string
					"org_id":             "", // Empty string
					"account_number":     "", // Empty string
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				Expect(result.User.Username).To(Equal("user-123")) // Falls back to sub
				Expect(result.User.Email).To(BeEmpty())
				Expect(result.OrgID).To(Equal("1"))         // Default
				Expect(result.AccountNumber).To(Equal("1")) // Default
			})

			It("should handle numeric values in string fields", func() {
				claims := map[string]interface{}{
					"sub":                "user-123",
					"preferred_username": "numeric.user",
					"org_id":             123, // Numeric instead of string
					"account_number":     456, // Numeric instead of string
				}

				token := createTestJWT(claims)
				req, _ := http.NewRequest("POST", "/upload", nil)
				req.Header.Set("X-ROS-Authenticated", "true")
				req.Header.Set("X-Bearer-Token", token)

				result, err := handler.extractIdentity(req)

				Expect(err).ToNot(HaveOccurred())
				// getStringClaim skips non-string values, so should use defaults
				Expect(result.OrgID).To(Equal("1"))
				Expect(result.AccountNumber).To(Equal("1"))
			})
		})
	})

	Context("Cluster Alias Logic", func() {
		var handler *Handler

		BeforeEach(func() {
			handler = &Handler{}
		})

		It("should use cluster alias when provided in manifest", func() {
			manifest := &Manifest{
				ClusterID:    "cluster-123",
				ClusterAlias: "production-cluster",
			}

			result := handler.getClusterAlias(manifest)

			Expect(result).To(Equal("production-cluster"))
		})

		It("should fallback to cluster ID when alias is empty", func() {
			manifest := &Manifest{
				ClusterID:    "cluster-456",
				ClusterAlias: "",
			}

			result := handler.getClusterAlias(manifest)

			Expect(result).To(Equal("cluster-456"))
		})

		It("should fallback to cluster ID when alias is not provided", func() {
			manifest := &Manifest{
				ClusterID: "cluster-789",
				// ClusterAlias not set (zero value)
			}

			result := handler.getClusterAlias(manifest)

			Expect(result).To(Equal("cluster-789"))
		})
	})
})
