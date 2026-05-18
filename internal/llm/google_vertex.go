package llm

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/oauth2/google"
)

const defaultVertexLocation = "us-central1"

type googleTokenSource struct {
	scopes []string
}

func (source googleTokenSource) Token(ctx context.Context) (string, error) {
	scopes := source.scopes
	if len(scopes) == 0 {
		scopes = []string{"https://www.googleapis.com/auth/cloud-platform"}
	}
	credentials, err := google.FindDefaultCredentials(ctx, scopes...)
	if err != nil {
		return "", err
	}
	token, err := credentials.TokenSource.Token()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func googleVertexProject() string {
	return firstEnv("OPENCODE_GOOGLE_VERTEX_PROJECT", "GOOGLE_VERTEX_PROJECT", "GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
}

func googleVertexLocation() string {
	return defaultString(firstEnv("OPENCODE_GOOGLE_VERTEX_LOCATION", "GOOGLE_VERTEX_LOCATION", "GOOGLE_CLOUD_LOCATION", "VERTEX_LOCATION"), defaultVertexLocation)
}

func googleVertexEndpoint(location string) string {
	if location == "global" {
		return "aiplatform.googleapis.com"
	}
	return location + "-aiplatform.googleapis.com"
}

func googleVertexGeminiBaseURL(project string, location string) string {
	location = defaultString(strings.TrimSpace(location), defaultVertexLocation)
	return fmt.Sprintf("https://%s/v1/projects/%s/locations/%s/publishers/google", googleVertexEndpoint(location), project, location)
}
