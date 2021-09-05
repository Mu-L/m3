package httpd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/m3db/m3/src/query/api/v1/options"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGraphiteRenderHandler(t *testing.T) {
	called := 0
	renderHandler := func(w http.ResponseWriter, req *http.Request) {
		called++
	}

	router := NewGraphiteRenderRouter()
	router.Setup(options.GraphiteRenderRouterOptions{
		RenderHandler: renderHandler,
	})
	rr := httptest.NewRecorder()

	req, err := http.NewRequest("GET", "/find?target=sum(metric)", nil)
	require.NoError(t, err)
	router.ServeHTTP(rr, req)
	assert.Equal(t, 1, called)
}

func TestGraphiteFindHandler(t *testing.T) {
	called := 0
	findHandler := func(w http.ResponseWriter, req *http.Request) {
		called++
	}

	router := NewGraphiteFindRouter()
	router.Setup(options.GraphiteFindRouterOptions{
		FindHandler: findHandler,
	})
	rr := httptest.NewRecorder()

	req, err := http.NewRequest("GET", "/render?target=sum(metric)", nil)
	require.NoError(t, err)
	router.ServeHTTP(rr, req)
	assert.Equal(t, 1, called)
}
