package server

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPIMatchesRoutes pins openapi/v1/openapi.yaml to the code route
// table. The spec is the public contract: a route added or removed without
// updating it must fail CI here.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../openapi/v1/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi spec: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatalf("openapi spec declares no paths")
	}

	specOps := map[string]bool{}
	for path, methods := range doc.Paths {
		for _, op := range []string{"get", "post", "put", "patch", "delete"} {
			if _, ok := methods[op]; ok {
				specOps[methodKey(op, path)] = true
			}
		}
	}

	codeOps := map[string]bool{}
	for _, r := range APIRoutes() {
		key := methodKey(opsName(r.Method), r.Path)
		codeOps[key] = true
		if !specOps[key] {
			t.Errorf("route %s %s is served but missing from the OpenAPI spec", r.Method, r.Path)
		}
	}
	for key := range specOps {
		if !codeOps[key] {
			t.Errorf("openapi path operation %s is documented but not implemented", key)
		}
	}
}

// Every path-ID parser can return the stable invalid_id problem. Keep that
// runtime boundary visible in the public contract for every operation that
// carries {id}, including reads whose other expected failure is uniform 404.
func TestOpenAPIPathIDOperationsDeclareBadRequest(t *testing.T) {
	raw, err := os.ReadFile("../../openapi/v1/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi spec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]any `yaml:"responses"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi spec: %v", err)
	}

	for _, route := range APIRoutes() {
		if !strings.Contains(route.Path, "{id}") {
			continue
		}
		method := opsName(route.Method)
		operation, ok := doc.Paths[route.Path][method]
		if !ok {
			t.Errorf("missing operation %s %s", route.Method, route.Path)
			continue
		}
		if _, ok := operation.Responses["400"]; !ok {
			t.Errorf("%s %s parses a path id but does not declare response 400", route.Method, route.Path)
		}
	}
}

func methodKey(method, path string) string { return method + " " + path }

func opsName(httpMethod string) string {
	switch httpMethod {
	case "GET":
		return "get"
	case "POST":
		return "post"
	case "PUT":
		return "put"
	case "PATCH":
		return "patch"
	case "DELETE":
		return "delete"
	default:
		return ""
	}
}
