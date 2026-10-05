package httpapi

import (
	"net/http"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

func load(t *testing.T, file string) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromFile(file)
	require.NoError(t, err)
	return spec
}

func index(t *testing.T, spec *openapi3.T) operations {
	t.Helper()
	ops, err := indexOperations(spec)
	require.NoError(t, err)
	return ops
}

// The whole contract, not only the generated tags: every operation must say
// how it is authenticated, and only the probes and the WebSocket, which has
// its own room ticket, may say "not at all".
func TestEveryOperationDeclaresSecurity(t *testing.T) {
	spec := load(t, "../../openapi.yaml")
	open := map[string]bool{}
	count := 0
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			count++
			scheme, err := securityOf(op)
			require.NoError(t, err, "%s %s (%s)", method, path, op.OperationID)
			if scheme == "" {
				open[op.OperationID] = true
			}
		}
	}
	t.Logf("%d operations checked", count)
	require.Equal(t, map[string]bool{"getHealthz": true, "getReadyz": true, "connectVideoRoom": true}, open)

	sessions := spec.Paths.Find("/auth/sessions").Post
	require.Equal(t, "createSession", sessions.OperationID)
	scheme, err := securityOf(sessions)
	require.NoError(t, err)
	require.Equal(t, schemeServiceKey, scheme, "sign-in takes the service key, not a session")
}

func TestIndexHoldsEachOperationsIDAndSecurity(t *testing.T) {
	require.Equal(t, operations{
		"GET /healthz":               {ID: "getHealthz", Security: schemeServiceKey},
		"GET /readyz":                {ID: "getReadyz", Security: ""},
		"GET /public/stories/{slug}": {ID: "getPublicStory", Security: schemeServiceKey},
		"GET /auth/me":               {ID: "getCurrentUser", Security: schemeSessionToken},
	}, index(t, load(t, "testdata/security.yaml")))
}

// The index is keyed by path template and looked up by chi route pattern; the
// two must be the same strings for every route the real router serves, and
// the ids must be the contract's, not the generated Go names.
func TestIndexKeysAreTheMountedRoutePatterns(t *testing.T) {
	spec, err := gen.GetSpec()
	require.NoError(t, err)
	ops := index(t, spec)

	mounted := map[string]bool{}
	err = chi.Walk(NewRouter(Deps{Log: quiet}).(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			mounted[operationKey(method, route)] = true
			return nil
		})
	require.NoError(t, err)
	require.NotEmpty(t, mounted)
	for key := range mounted {
		require.Contains(t, ops, key)
	}
	for key := range ops {
		require.Contains(t, mounted, key)
	}
	require.Equal(t, "getHealthz", ops["GET /healthz"].ID)
}

// thingSpec is a one-operation spec whose operation declares security, a
// line of YAML, under a document-wide default it must not inherit.
func thingSpec(t *testing.T, security string) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData([]byte(`
openapi: "3.1.0"
info: {title: t, version: "0"}
security: [{serviceKey: []}]
paths:
  /things/{id}:
    get:
      operationId: getThing
      ` + security + `
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses: {"200": {description: ok}}
`))
	require.NoError(t, err)
	return spec
}

func TestIndexRejectsUnclearSecurity(t *testing.T) {
	for name, security := range map[string]string{
		"undeclared, despite a document default": "",
		"two schemes together":                   "security: [{serviceKey: [], sessionToken: []}]",
		"either of two schemes":                  "security: [{serviceKey: []}, {sessionToken: []}]",
		"an empty requirement":                   "security: [{}]",
		"an unknown scheme":                      "security: [{apiKey: []}]",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := indexOperations(thingSpec(t, security))
			require.ErrorIs(t, err, errUnclearSecurity)
			require.ErrorContains(t, err, "GET /things/{id}")
		})
	}
}

func TestMountRefusesUnclearSecurity(t *testing.T) {
	require.Panics(t, func() { mountAPI(&server{}, thingSpec(t, ""), testServiceKey, noRedis, quiet) })
	require.NotPanics(t, func() {
		mountAPI(&server{}, thingSpec(t, "security: [{serviceKey: []}]"), testServiceKey, noRedis, quiet)
	})
}
